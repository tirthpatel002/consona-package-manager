package sshremote

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// HostEntry is one concrete Host alias from an ssh config file.
type HostEntry struct {
	Alias    string
	HostName string // "" if not set (ssh then uses the alias)
	User     string
}

// SSHConfigPath is the client config consona reads for auto-detection and
// passes to ssh: $CONSONA_SSH_CONFIG (also given to ssh as -F), else
// ~/.ssh/config.
func SSHConfigPath() string {
	if p := os.Getenv("CONSONA_SSH_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".ssh", "config")
}

// gitForges are never probed: they accept ssh but are not machines.
var gitForges = []string{"github.com", "gitlab.com", "bitbucket.org", "ssh.dev.azure.com", "vs-ssh.visualstudio.com", "codeberg.org", "git.sr.ht", "source.developers.google.com"}

func isForge(h string) bool {
	h = strings.ToLower(h)
	for _, f := range gitForges {
		if h == f || strings.HasSuffix(h, "."+f) {
			return true
		}
	}
	return false
}

// ParseSSHConfig returns concrete Host aliases (no wildcards or negations)
// in file order, following Include. Git forges and hosts whose User is
// "git" are skipped, since they are not machines that can run consona.
func ParseSSHConfig(path string) []HostEntry {
	var out []HostEntry
	seen := map[string]bool{}
	parseFile(path, 0, &out, seen)
	var keep []HostEntry
	for _, h := range out {
		if isForge(h.Alias) || isForge(h.HostName) || h.User == "git" {
			continue
		}
		keep = append(keep, h)
	}
	return keep
}

func parseFile(path string, depth int, out *[]HostEntry, seen map[string]bool) {
	if depth > 8 {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	var block []int // indexes into *out for the current Host block
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		key, val := splitKV(line)
		switch strings.ToLower(key) {
		case "host":
			block = nil
			for _, a := range fields(val) {
				if a == "" || strings.ContainsAny(a, "*?!") || seen[a] {
					continue
				}
				seen[a] = true
				*out = append(*out, HostEntry{Alias: a})
				block = append(block, len(*out)-1)
			}
		case "match":
			block = nil
		case "hostname":
			for _, i := range block {
				if (*out)[i].HostName == "" { // first value wins, like ssh
					(*out)[i].HostName = val
				}
			}
		case "user":
			for _, i := range block {
				if (*out)[i].User == "" {
					(*out)[i].User = val
				}
			}
		case "include":
			for _, pat := range fields(val) {
				if strings.HasPrefix(pat, "~/") {
					h, _ := os.UserHomeDir()
					pat = filepath.Join(h, pat[2:])
				} else if !filepath.IsAbs(pat) {
					pat = filepath.Join(filepath.Dir(path), pat)
				}
				ms, _ := filepath.Glob(pat)
				for _, m := range ms {
					parseFile(m, depth+1, out, seen)
				}
			}
		}
	}
}

func splitKV(line string) (string, string) {
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return line, ""
	}
	k := line[:i]
	v := strings.TrimLeft(line[i:], " \t")
	v = strings.TrimPrefix(v, "=")
	return k, strings.TrimSpace(v)
}

// fields splits on whitespace, honouring double quotes.
func fields(s string) []string {
	var out []string
	var cur strings.Builder
	q := false
	for _, r := range s {
		switch {
		case r == '"':
			q = !q
		case (r == ' ' || r == '\t') && !q:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
