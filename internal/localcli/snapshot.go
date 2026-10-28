package localcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
	"github.com/tirthpatel002/consona-package-manager/internal/packmap"
	"github.com/tirthpatel002/consona-package-manager/internal/snapshot"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"
)

const snapshotUsage = `usage:
  consona snapshot create [<owner/repo> ...] [--out FILE]   record repos' head+tags (default: every mapped repo)
  consona snapshot update [FILE]                            re-capture the repos in FILE and show what changed
  consona snapshot use [FILE]                               load FILE on this device and route the project's .npmrc through it
  consona snapshot unuse [FILE]                             remove the managed block from the project's .npmrc
  consona snapshot show [FILE]                              print what FILE records
FILE defaults to ./` + snapshot.FileName

func cmdSnapshot(ctx context.Context, args []string) int {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "create":
		pos, opt, err := flags(args, "out")
		if err != nil {
			return fail("%v", err)
		}
		out := snapshot.FileName
		if v := opt["out"]; v != "" {
			out = v
		}
		repos, scopes, err := snapshotTargets(pos)
		if err != nil {
			return fail("%v", err)
		}
		return writeCapture(ctx, out, repos, scopes, nil)
	case "update":
		file := fileArg(args)
		old, err := loadFile(file)
		if err != nil {
			return fail("%v", err)
		}
		repos := make([]string, 0, len(old.Repos))
		for r := range old.Repos {
			repos = append(repos, r)
		}
		sort.Strings(repos)
		return writeCapture(ctx, file, repos, old.Scopes, old)
	case "show":
		file := fileArg(args)
		data, err := snapshot.ReadFile(file)
		if err != nil {
			return fail("%v", err)
		}
		s, err := snapshot.Parse(data)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("snapshot %s\nscopes: @%s\n", snapshot.Hash(data), strings.Join(s.Scopes, ", @"))
		for _, r := range sortedRepos(s) {
			rec := s.Repos[r]
			fmt.Printf("  %s  head %.7s  %d tag(s)  captured %s\n", r, rec.Head, len(rec.Tags), rec.CaptureAt.Format(time.RFC3339))
		}
		return 0
	case "use":
		return useSnapshot(fileArg(args))
	case "unuse":
		return unuseSnapshot(fileArg(args))
	}
	fmt.Fprintln(os.Stderr, snapshotUsage)
	return 2
}

func fileArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return snapshot.FileName
}

func loadFile(file string) (*snapshot.Snapshot, error) {
	data, err := snapshot.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return snapshot.Parse(data)
}

func sortedRepos(s *snapshot.Snapshot) []string {
	var out []string
	for r := range s.Repos {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// snapshotTargets resolves which repos and npm scopes a new snapshot
// covers: the named repos, or every mapped repo. A scope is routed through
// the snapshot when it belongs to a covered repo (its own owner, for a
// repo-level package, or the scope of a package mapped to that repo).
func snapshotTargets(named []string) (repos, scopes []string, err error) {
	entries, err := packmap.Open().List()
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	sc := map[string]bool{}
	add := func(r string) {
		if !seen[r] {
			seen[r] = true
			repos = append(repos, r)
		}
	}
	if len(named) == 0 {
		for _, e := range entries {
			add(strings.ToLower(e.Repo))
		}
	}
	for _, n := range named {
		n = strings.ToLower(strings.TrimPrefix(n, "@"))
		owner, repo, ok := strings.Cut(n, "/")
		if !ok || owner == "" || repo == "" {
			return nil, nil, fmt.Errorf("invalid repo %q: want owner/repo", n)
		}
		add(n)
		sc[owner] = true // a repo-level package @owner/repo
	}
	for _, e := range entries {
		if seen[strings.ToLower(e.Repo)] {
			if s, _, err := localindex.SplitName(e.Name); err == nil {
				sc[s] = true
			}
		}
	}
	if len(repos) == 0 {
		return nil, nil, fmt.Errorf("nothing to snapshot: name repos (consona snapshot create <owner/repo>) or map folders first")
	}
	for s := range sc {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	sort.Strings(repos)
	return repos, scopes, nil
}

func writeCapture(ctx context.Context, out string, repos, scopes []string, old *snapshot.Snapshot) int {
	gh := github.New()
	s := &snapshot.Snapshot{Version: snapshot.SchemaVersion, Scopes: scopes, Repos: map[string]snapshot.Repo{}}
	if entries, err := packmap.Open().List(); err == nil {
		for _, e := range entries {
			for _, r := range repos {
				if strings.EqualFold(e.Repo, r) {
					if s.Folders == nil {
						s.Folders = map[string]snapshot.Folder{}
					}
					s.Folders[strings.ToLower(e.Name)] = snapshot.Folder{Repo: strings.ToLower(e.Repo), Path: e.Path}
				}
			}
		}
	}
	for _, name := range repos {
		owner, repo, _ := strings.Cut(name, "/")
		ref := core.Ref{Owner: owner, Name: repo}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		rec, err := snapshot.Capture(cctx, func(c context.Context) ([]core.Tag, string, error) { return gh.Capture(c, ref) }, 3,
			func(n int) { time.Sleep(time.Duration(n) * 700 * time.Millisecond) })
		cancel()
		if err != nil {
			return fail("%s: %v (nothing written)", name, err)
		}
		s.Repos[name] = rec
	}
	if old != nil {
		printDiff(old, s)
		for n, f := range s.Folders {
			if of, ok := old.Folders[n]; !ok {
				fmt.Printf("  mapping %s -> %s/%s is new\n", n, f.Repo, f.Path)
			} else if of != f {
				fmt.Printf("  mapping %s CHANGED: %s/%s -> %s/%s\n", n, of.Repo, of.Path, f.Repo, f.Path)
			}
		}
		for n := range old.Folders {
			if _, ok := s.Folders[n]; !ok {
				fmt.Printf("  mapping %s is no longer in the folder map\n", n)
			}
		}
	}
	data, err := snapshot.Marshal(s)
	if err != nil {
		return fail("%v", err)
	}
	if _, err := snapshot.Parse(data); err != nil {
		return fail("captured state failed validation: %v", err)
	}
	if err := writeFileAtomic(out, data); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("consona: wrote %s (%d repo(s), id %.12s). Each repo was read twice and written only if both reads agreed.\n", out, len(s.Repos), snapshot.Hash(data))
	fmt.Println("consona: run `consona snapshot use` to route this project through it, and commit the file.")
	return 0
}

func printDiff(old, cur *snapshot.Snapshot) {
	for _, r := range sortedRepos(cur) {
		n, o := cur.Repos[r], old.Repos[r]
		var ch []string
		if o.Head != n.Head {
			ch = append(ch, fmt.Sprintf("head %.7s -> %.7s", o.Head, n.Head))
		}
		for t, c := range n.Tags {
			if oc, ok := o.Tags[t]; !ok {
				ch = append(ch, "new tag "+t)
			} else if oc != c {
				ch = append(ch, fmt.Sprintf("tag %s MOVED %.7s -> %.7s", t, oc, c))
			}
		}
		for t := range o.Tags {
			if _, ok := n.Tags[t]; !ok {
				ch = append(ch, "tag removed "+t)
			}
		}
		sort.Strings(ch)
		if len(ch) == 0 {
			fmt.Printf("  %s: unchanged\n", r)
		} else {
			fmt.Printf("  %s: %s\n", r, strings.Join(ch, "; "))
		}
	}
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".consona-tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const (
	rcStart = "# consona snapshot (managed by `consona snapshot use`)"
	rcEnd   = "# end consona snapshot"
)

// useSnapshot stores the file by hash and points the project's .npmrc
// (the directory of the snapshot file) at the daemon's snapshot URL for the
// snapshot's scopes. The URL carries the snapshot, so the daemon needs no
// way to identify the client.
func useSnapshot(file string) int {
	data, err := snapshot.ReadFile(file)
	if err != nil {
		return fail("%v", err)
	}
	s, err := snapshot.Parse(data)
	if err != nil {
		return fail("%v", err)
	}
	hash, err := snapshot.Save(data)
	if err != nil {
		return fail("%v", err)
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return fail("%v", err)
	}
	rc := filepath.Join(filepath.Dir(abs), ".npmrc")
	var old string
	if b, err := os.ReadFile(rc); err == nil {
		old = string(b)
	} else if !os.IsNotExist(err) {
		return fail("%v", err)
	}
	if clash := foreignScopeLines(old, s.Scopes); len(clash) > 0 {
		return fail("%s already sets %s outside consona's block; remove or change those lines first (consona will not override your registry settings)", rc, strings.Join(clash, ", "))
	}
	base := daemon.Load().BaseURL()
	var block strings.Builder
	block.WriteString(rcStart + "\n")
	for _, sc := range s.Scopes {
		fmt.Fprintf(&block, "@%s:registry=%s/npm/s/%s/\n", sc, base, hash)
	}
	block.WriteString(rcEnd + "\n")
	if err := writeFileAtomic(rc, []byte(replaceBlock(old, block.String()))); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("consona: snapshot %.12s loaded; %s now routes @%s through it.\n", hash, rc, strings.Join(s.Scopes, ", @"))
	fmt.Println("consona: installs in this project resolve those scopes from the recorded repo state. A repo missing from the snapshot is an error, never a live lookup.")
	return 0
}

// replaceBlock puts block in text, replacing a previous managed block.
func replaceBlock(text, block string) string {
	if i := strings.Index(text, rcStart); i >= 0 {
		if j := strings.Index(text[i:], rcEnd); j >= 0 {
			end := i + j + len(rcEnd)
			end += len(text[end:]) - len(strings.TrimPrefix(text[end:], "\n"))
			return text[:i] + block + text[end:]
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + block
}

// foreignScopeLines lists "@scope:registry" settings for the snapshot's
// scopes found outside the managed block.
func foreignScopeLines(text string, scopes []string) []string {
	if i := strings.Index(text, rcStart); i >= 0 {
		if j := strings.Index(text[i:], rcEnd); j >= 0 {
			text = text[:i] + text[i+j+len(rcEnd):]
		}
	}
	want := map[string]bool{}
	for _, sc := range scopes {
		want["@"+sc+":registry"] = true
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && want[strings.ToLower(strings.TrimSpace(k))] {
			out = append(out, strings.TrimSpace(k))
		}
	}
	return out
}

// unuseSnapshot removes the managed block, leaving every other line alone.
func unuseSnapshot(file string) int {
	abs, err := filepath.Abs(file)
	if err != nil {
		return fail("%v", err)
	}
	rc := filepath.Join(filepath.Dir(abs), ".npmrc")
	b, err := os.ReadFile(rc)
	if err != nil {
		return fail("%v", err)
	}
	text := string(b)
	i := strings.Index(text, rcStart)
	j := strings.Index(text, rcEnd)
	if i < 0 || j < i {
		fmt.Println("consona: no snapshot block in", rc)
		return 0
	}
	end := j + len(rcEnd)
	end += len(text[end:]) - len(strings.TrimPrefix(text[end:], "\n"))
	if err := writeFileAtomic(rc, []byte(text[:i]+text[end:])); err != nil {
		return fail("%v", err)
	}
	fmt.Println("consona: removed the snapshot block from", rc)
	return 0
}
