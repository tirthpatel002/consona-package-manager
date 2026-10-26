package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	npmadapter "github.com/tirthpatel002/consona-package-manager/internal/adapter/npm"
	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/localcli"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/redact"
)

const whyUsage = `consona why: show provenance and build metadata for an installed package

  consona why <@scope/pkg> [--dir DIR] [--json]

Shows where the package came from (local folder / ssh remote / github), repo,
commit, git tag, build time, integrity, resolved URL and recorded warnings.

Flags:
  --dir DIR     project directory containing package-lock.json (default ".")
  --json        output machine-readable JSON
  --quiet       suppress progress on stderr
`

const reportUsage = `consona report: provenance report for all private packages in a project

  consona report [--dir DIR] [--out FILE] [--json]

Finds all private packages in package-lock.json (resolved to consona daemon or
routed scopes) and produces a Markdown table or JSON report.

Flags:
  --dir DIR     project directory containing package-lock.json (default ".")
  --out FILE    output file path (atomic write, mode 0644; default stdout)
  --json        output machine-readable JSON array
  --quiet       suppress progress on stderr
`

// PackageProvenance contains all provenance information for a package.
type PackageProvenance struct {
	Package   string   `json:"package"`
	Version   string   `json:"version"`
	Source    string   `json:"source"`
	Repo      string   `json:"repo"`
	Commit    string   `json:"commit"`
	Tag       string   `json:"tag"`
	Built     string   `json:"built"`
	Integrity string   `json:"integrity"`
	Resolved  string   `json:"resolved"`
	Warnings  []string `json:"warnings"`
}

// Human formats the provenance as aligned key: value lines on stdout.
func (p PackageProvenance) Human() string {
	warnings := "none"
	if len(p.Warnings) > 0 {
		warnings = strings.Join(p.Warnings, "; ")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Package:", p.Package))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Version:", p.Version))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Source:", p.Source))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Repo:", p.Repo))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Commit:", p.Commit))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Tag:", p.Tag))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Built:", p.Built))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Integrity:", p.Integrity))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Resolved:", p.Resolved))
	sb.WriteString(fmt.Sprintf("%-11s %s\n", "Warnings:", warnings))
	return sb.String()
}

type lockfilePackage struct {
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
}

type lockfileData struct {
	Name            string                     `json:"name"`
	LockfileVersion int                        `json:"lockfileVersion"`
	Packages        map[string]lockfilePackage `json:"packages"`
	Dependencies    map[string]lockfilePackage `json:"dependencies"`
}

type cachedBuild struct {
	Commit    string
	Version   string
	Integrity string
	SHA1      string
	Built     time.Time
}

func runWhy(cfg daemon.Config, args []string) int {
	if len(args) == 0 {
		return fail("subcommand required: why or report")
	}
	cmd := args[0]
	args = args[1:]
	if cmd == "why" {
		return cmdWhy(cfg, args)
	}
	if cmd == "report" {
		return cmdReport(cfg, args)
	}
	return fail("unknown command %q (expected why or report)", cmd)
}

func cmdWhy(cfg daemon.Config, args []string) int {
	var (
		pkgName string
		dir     = "."
		asJSON  bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "help" || a == "--help" || a == "-h":
			fmt.Print(whyUsage)
			return 0
		case a == "--json":
			asJSON = true
		case a == "--dir" && i+1 < len(args):
			i++
			dir = args[i]
		case strings.HasPrefix(a, "--dir="):
			dir = strings.TrimPrefix(a, "--dir=")
		case !strings.HasPrefix(a, "-") && pkgName == "":
			pkgName = a
		default:
			return fail("unknown argument %q (see \"consona why help\")", a)
		}
	}
	if pkgName == "" {
		return fail("package name required (see \"consona why help\")")
	}

	say("inspecting provenance for %s in %s", pkgName, dir)
	lockPath := filepath.Join(dir, "package-lock.json")
	lockBs, err := os.ReadFile(lockPath)
	if err != nil {
		return fail("package-lock.json not found in %s: %v", dir, err)
	}
	var lock lockfileData
	if err := json.Unmarshal(lockBs, &lock); err != nil {
		return fail("reading %s: %v", lockPath, err)
	}

	lp, ok := findPackageInLockfile(lock, dir, pkgName)
	if !ok {
		return fail("package %q is not in package-lock.json", pkgName)
	}

	store, err := openLogStoreReadOnly()
	if err != nil {
		say("warning: could not open log store: %v", err)
	}
	if store != nil {
		defer store.Close()
	}

	prov := resolveProvenance(cfg, store, pkgName, lp)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(prov); err != nil {
			return fail("json encode: %v", err)
		}
		return 0
	}

	fmt.Print(prov.Human())
	return 0
}

func cmdReport(cfg daemon.Config, args []string) int {
	var (
		dir     = "."
		outFile string
		asJSON  bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "help" || a == "--help" || a == "-h":
			fmt.Print(reportUsage)
			return 0
		case a == "--json":
			asJSON = true
		case a == "--dir" && i+1 < len(args):
			i++
			dir = args[i]
		case strings.HasPrefix(a, "--dir="):
			dir = strings.TrimPrefix(a, "--dir=")
		case (a == "--out" || a == "-o") && i+1 < len(args):
			i++
			outFile = args[i]
		case strings.HasPrefix(a, "--out="):
			outFile = strings.TrimPrefix(a, "--out=")
		case strings.HasPrefix(a, "-o="):
			outFile = strings.TrimPrefix(a, "-o=")
		default:
			return fail("unknown argument %q (see \"consona report help\")", a)
		}
	}

	say("generating provenance report for %s", dir)
	lockPath := filepath.Join(dir, "package-lock.json")
	lockBs, err := os.ReadFile(lockPath)
	if err != nil {
		return fail("package-lock.json not found in %s: %v", dir, err)
	}
	var lock lockfileData
	if err := json.Unmarshal(lockBs, &lock); err != nil {
		return fail("reading %s: %v", lockPath, err)
	}

	privPkgs := findPrivatePackages(lock, dir, cfg)
	say("found %d private package(s)", len(privPkgs))

	store, err := openLogStoreReadOnly()
	if err != nil {
		say("warning: could not open log store: %v", err)
	}
	if store != nil {
		defer store.Close()
	}

	var names []string
	for n := range privPkgs {
		names = append(names, n)
	}
	sort.Strings(names)

	provs := make([]PackageProvenance, 0, len(names))
	for _, n := range names {
		provs = append(provs, resolveProvenance(cfg, store, n, privPkgs[n]))
	}

	var data []byte
	if asJSON {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(provs); err != nil {
			return fail("json encode: %v", err)
		}
		data = buf.Bytes()
	} else {
		data = []byte(formatMarkdownReport(provs))
	}

	if outFile == "" || outFile == "-" {
		_, err := os.Stdout.Write(data)
		if err != nil {
			return fail("writing output: %v", err)
		}
		return 0
	}

	if err := writeAtomic(outFile, data); err != nil {
		return fail("writing %s: %v", outFile, err)
	}
	say("wrote report to %s", outFile)
	return 0
}

func formatMarkdownReport(provs []PackageProvenance) string {
	var sb strings.Builder
	sb.WriteString("# Package Provenance Report\n\n")
	sb.WriteString("| Package | Version | Source | Repo | Commit | Tag | Built | Integrity | Resolved | Warnings |\n")
	sb.WriteString("|---------|---------|--------|------|--------|-----|-------|-----------|----------|----------|\n")
	for _, p := range provs {
		warnings := "none"
		if len(p.Warnings) > 0 {
			warnings = strings.Join(p.Warnings, "; ")
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			escapeMD(p.Package),
			escapeMD(p.Version),
			escapeMD(p.Source),
			escapeMD(p.Repo),
			escapeMD(p.Commit),
			escapeMD(p.Tag),
			escapeMD(p.Built),
			escapeMD(p.Integrity),
			escapeMD(p.Resolved),
			escapeMD(warnings),
		))
	}
	return sb.String()
}

func escapeMD(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".report-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func findPackageInLockfile(lock lockfileData, dir, name string) (lockfilePackage, bool) {
	norm := strings.ToLower(name)
	targetKey := "node_modules/" + norm
	for k, p := range lock.Packages {
		kLower := strings.ToLower(k)
		if kLower == targetKey || strings.HasSuffix(kLower, "/"+targetKey) {
			fillFromNodeModules(dir, name, &p)
			return p, true
		}
	}
	for k, p := range lock.Dependencies {
		if strings.ToLower(k) == norm {
			fillFromNodeModules(dir, name, &p)
			return p, true
		}
	}
	return lockfilePackage{}, false
}

func fillFromNodeModules(dir, name string, p *lockfilePackage) {
	pjPath := filepath.Join(dir, "node_modules", filepath.FromSlash(name), "package.json")
	if bs, err := os.ReadFile(pjPath); err == nil {
		var pj struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(bs, &pj) == nil && pj.Version != "" {
			if p.Version == "" {
				p.Version = pj.Version
			}
		}
	}
}

func findPrivatePackages(lock lockfileData, dir string, cfg daemon.Config) map[string]lockfilePackage {
	routedScopes := map[string]bool{}
	for s := range npmadapter.RoutedScopes() {
		routedScopes[strings.TrimPrefix(strings.ToLower(s), "@")] = true
	}
	for _, s := range localcli.ExtraOwners() {
		routedScopes[strings.TrimPrefix(strings.ToLower(s), "@")] = true
	}

	isPrivate := func(pkgName, resolved string) bool {
		if resolved != "" {
			if strings.Contains(resolved, "/npm/") && (strings.Contains(resolved, "127.0.0.1:") || strings.Contains(resolved, "localhost:") || strings.HasPrefix(resolved, cfg.BaseURL())) {
				return true
			}
		}
		if strings.HasPrefix(pkgName, "@") {
			parts := strings.SplitN(pkgName[1:], "/", 2)
			if len(parts) == 2 {
				scope := strings.TrimPrefix(strings.ToLower(parts[0]), "@")
				if routedScopes[scope] {
					return true
				}
			}
		}
		return false
	}

	result := map[string]lockfilePackage{}
	for k, p := range lock.Packages {
		if k == "" {
			continue
		}
		pkgName := packageNameFromKey(k)
		if pkgName != "" && isPrivate(pkgName, p.Resolved) {
			fillFromNodeModules(dir, pkgName, &p)
			result[pkgName] = p
		}
	}
	for k, p := range lock.Dependencies {
		if isPrivate(k, p.Resolved) {
			if _, exists := result[k]; !exists {
				fillFromNodeModules(dir, k, &p)
				result[k] = p
			}
		}
	}
	return result
}

func packageNameFromKey(k string) string {
	idx := strings.LastIndex(k, "node_modules/")
	if idx >= 0 {
		return k[idx+len("node_modules/"):]
	}
	return ""
}

func openLogStoreReadOnly() (logstore.Store, error) {
	dir := config.LogDir()
	dbPath := filepath.Join(dir, "events.db")
	jsonlPath := filepath.Join(dir, "jsonl")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			return nil, nil
		}
	}
	return logstore.Open(dir)
}

func scanBuildCache(home, owner, name string) []cachedBuild {
	candidates := []string{
		filepath.Join(home, "cache", "npm", owner, name),
	}
	lower := filepath.Join(home, "cache", "npm", strings.ToLower(owner), strings.ToLower(name))
	if lower != candidates[0] {
		candidates = append(candidates, lower)
	}

	var builds []cachedBuild
	seen := map[string]bool{}
	for _, base := range candidates {
		commits, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, cd := range commits {
			if !cd.IsDir() {
				continue
			}
			commit := cd.Name()
			versions, err := os.ReadDir(filepath.Join(base, commit))
			if err != nil {
				continue
			}
			for _, vd := range versions {
				if !vd.IsDir() {
					continue
				}
				key := commit + "|" + vd.Name()
				if seen[key] {
					continue
				}
				seen[key] = true

				versionDir := filepath.Join(base, commit, vd.Name())
				artPath := filepath.Join(versionDir, "artifact.json")
				bs, err := os.ReadFile(artPath)
				if err != nil {
					continue
				}
				var art core.Artifact
				if err := json.Unmarshal(bs, &art); err != nil {
					continue
				}
				builtTime := time.Time{}
				if fi, err := os.Stat(artPath); err == nil {
					builtTime = fi.ModTime().UTC()
				}
				if art.File != "" {
					if afi, err := os.Stat(filepath.Join(versionDir, art.File)); err == nil {
						builtTime = afi.ModTime().UTC()
					}
				}
				integrity := ""
				if art.SHA512 != "" {
					integrity = "sha512-" + art.SHA512
				}
				ver := art.Version
				if ver == "" {
					ver = vd.Name()
				}
				builds = append(builds, cachedBuild{
					Commit:    commit,
					Version:   ver,
					Integrity: integrity,
					SHA1:      art.SHA1,
					Built:     builtTime,
				})
			}
		}
	}
	sort.Slice(builds, func(i, j int) bool {
		return builds[i].Built.After(builds[j].Built)
	})
	return builds
}

func splitScopePkg(name string) (string, string) {
	name = strings.TrimPrefix(name, "@")
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", parts[0]
}

func extractSourceAndRepo(e logstore.Entry) (source string, repo string) {
	var kind, where string
	if e.Fields != nil {
		if k, ok := e.Fields["source"].(string); ok {
			kind = k
		}
		if w, ok := e.Fields["where"].(string); ok {
			where = w
		}
	}
	if kind == "" && e.Message != "" {
		parts := strings.SplitN(e.Message, " ", 2)
		if len(parts) == 2 {
			switch parts[0] {
			case "local", "ssh", "github":
				kind = parts[0]
				where = parts[1]
			}
		}
	}
	switch strings.ToLower(kind) {
	case "local", "local folder":
		source = "local folder"
		repo = where
	case "ssh", "ssh remote":
		source = "ssh remote"
		repo = where
	case "github":
		source = "github"
		repo = where
		repo = strings.TrimPrefix(repo, "https://")
		repo = strings.TrimPrefix(repo, "http://")
		repo = strings.TrimPrefix(repo, "github.com/")
		if repo == "" && e.Owner != "" && e.Repo != "" {
			repo = e.Owner + "/" + e.Repo
		}
	default:
		if kind != "" {
			source = kind
			repo = where
		}
	}
	return source, repo
}

func resolveProvenance(cfg daemon.Config, store logstore.Store, pkgName string, lp lockfilePackage) PackageProvenance {
	prov := PackageProvenance{
		Package:   pkgName,
		Version:   lp.Version,
		Source:    "unknown",
		Repo:      "unknown",
		Commit:    "unknown",
		Tag:       "unknown",
		Built:     "unknown",
		Integrity: lp.Integrity,
		Resolved:  lp.Resolved,
		Warnings:  []string{},
	}
	if prov.Version == "" {
		prov.Version = "unknown"
	}
	if prov.Integrity == "" {
		prov.Integrity = "unknown"
	}
	if prov.Resolved == "" {
		prov.Resolved = "unknown"
	}

	scope, repoName := splitScopePkg(pkgName)
	cachedBuilds := scanBuildCache(cfg.Home, scope, repoName)

	var entries []logstore.Entry
	if store != nil {
		if es, err := store.Query(logstore.Filter{Package: pkgName}); err == nil {
			entries = es
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].TS.Equal(entries[j].TS) {
			return entries[i].ID > entries[j].ID
		}
		return entries[i].TS.After(entries[j].TS)
	})

	var matchedBuild *cachedBuild
	if lp.Integrity != "" && lp.Integrity != "unknown" {
		for i := range cachedBuilds {
			if cachedBuilds[i].Integrity != "" && cachedBuilds[i].Integrity == lp.Integrity {
				matchedBuild = &cachedBuilds[i]
				break
			}
		}
	}
	if matchedBuild == nil && lp.Version != "" && lp.Version != "unknown" {
		for i := range cachedBuilds {
			if cachedBuilds[i].Version == lp.Version {
				matchedBuild = &cachedBuilds[i]
				break
			}
		}
	}
	if matchedBuild != nil {
		if matchedBuild.Commit != "" {
			prov.Commit = matchedBuild.Commit
		}
		if !matchedBuild.Built.IsZero() {
			prov.Built = matchedBuild.Built.UTC().Format(time.RFC3339)
		}
		if prov.Integrity == "unknown" && matchedBuild.Integrity != "" {
			prov.Integrity = matchedBuild.Integrity
		}
	}

	// 1. Source / Repo from EvSourceResolve matching version first
	for _, e := range entries {
		if e.Event == logstore.EvSourceResolve {
			verMatch := (lp.Integrity != "" && lp.Integrity != "unknown" && e.Fields != nil && e.Fields["integrity"] == lp.Integrity) ||
				(lp.Version != "" && lp.Version != "unknown" && e.Version == lp.Version)
			if verMatch {
				source, repo := extractSourceAndRepo(e)
				if source != "" && source != "unknown" {
					prov.Source = source
				}
				if repo != "" && repo != "unknown" {
					prov.Repo = repo
				}
				if prov.Source != "unknown" {
					break
				}
			}
		}
	}
	// Fallback to any EvSourceResolve for this package
	if prov.Source == "unknown" {
		for _, e := range entries {
			if e.Event == logstore.EvSourceResolve {
				source, repo := extractSourceAndRepo(e)
				if source != "" && source != "unknown" {
					prov.Source = source
				}
				if repo != "" && repo != "unknown" {
					prov.Repo = repo
				}
				if prov.Source != "unknown" {
					break
				}
			}
		}
	}

	// 2. Commit, Built, Tag matching version/integrity first
	for _, e := range entries {
		verMatch := (lp.Integrity != "" && lp.Integrity != "unknown" && e.Fields != nil && e.Fields["integrity"] == lp.Integrity) ||
			(lp.Version != "" && lp.Version != "unknown" && e.Version == lp.Version)
		if !verMatch {
			continue
		}
		if prov.Commit == "unknown" {
			if e.CommitSHA != "" {
				prov.Commit = e.CommitSHA
			} else if e.Fields != nil {
				if c, ok := e.Fields["commit"].(string); ok && c != "" {
					prov.Commit = c
				} else if c, ok := e.Fields["commit_sha"].(string); ok && c != "" {
					prov.Commit = c
				}
			}
		}
		if prov.Built == "unknown" && e.Event == logstore.EvBuildFinish && !e.TS.IsZero() {
			prov.Built = e.TS.UTC().Format(time.RFC3339)
		}
		if prov.Tag == "unknown" && e.Fields != nil {
			if t, ok := e.Fields["tag"].(string); ok && t != "" {
				prov.Tag = t
			} else if t, ok := e.Fields["tag_name"].(string); ok && t != "" {
				prov.Tag = t
			}
		}
	}

	// Fallback for Commit, Built, Tag to any event for package if still unknown
	if prov.Commit == "unknown" || prov.Built == "unknown" || prov.Tag == "unknown" {
		for _, e := range entries {
			if prov.Commit == "unknown" {
				if e.CommitSHA != "" {
					prov.Commit = e.CommitSHA
				} else if e.Fields != nil {
					if c, ok := e.Fields["commit"].(string); ok && c != "" {
						prov.Commit = c
					} else if c, ok := e.Fields["commit_sha"].(string); ok && c != "" {
						prov.Commit = c
					}
				}
			}
			if prov.Built == "unknown" && e.Event == logstore.EvBuildFinish && !e.TS.IsZero() {
				prov.Built = e.TS.UTC().Format(time.RFC3339)
			}
			if prov.Tag == "unknown" && e.Fields != nil {
				if t, ok := e.Fields["tag"].(string); ok && t != "" {
					prov.Tag = t
				} else if t, ok := e.Fields["tag_name"].(string); ok && t != "" {
					prov.Tag = t
				}
			}
		}
	}

	if prov.Commit == "unknown" && lp.Version != "" {
		if idx := strings.Index(lp.Version, ".h"); idx >= 0 {
			prov.Commit = lp.Version[idx+2:]
		}
	}

	if prov.Source == "unknown" {
		for _, e := range entries {
			if e.Owner != "" && e.Repo != "" {
				prov.Source = "github"
				if prov.Repo == "unknown" {
					prov.Repo = e.Owner + "/" + e.Repo
				}
				break
			}
		}
	}
	if prov.Source == "github" && prov.Repo == "unknown" && scope != "" && repoName != "" {
		prov.Repo = scope + "/" + repoName
	}

	seenWarnings := map[string]bool{}
	for _, e := range entries {
		isWarn := e.Level == "warn" || e.Level == "error" || e.Result == "warn" ||
			e.Event == logstore.EvSourceNameMismatch || e.Event == logstore.EvSourceTagMoved ||
			e.Event == logstore.EvSourceFail
		if !isWarn {
			continue
		}
		if e.Version != "" && lp.Version != "" && lp.Version != "unknown" && e.Version != lp.Version {
			continue
		}
		msg := strings.TrimSpace(redact.String(e.Message))
		if msg != "" && !seenWarnings[msg] {
			seenWarnings[msg] = true
			prov.Warnings = append(prov.Warnings, msg)
		}
	}

	return prov
}
