package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

var cacheUsage = `consona cache: manage the local build cache

  consona cache list [--json]          list cached builds (newest first, with total size)
  consona cache prune [--older-than D] [--dry-run]
                                       delete cache entries older than D (default 30d; e.g. 7d, 24h, 30m)
`

// CacheEntry represents one cached build artifact.
type CacheEntry struct {
	Package string    `json:"package"`
	Version string    `json:"version"`
	Commit  string    `json:"commit"`
	Size    int64     `json:"size"`
	Built   time.Time `json:"built"`
	Path    string    `json:"path"`
	Dir     string    `json:"-"`
}

type cacheListOutput struct {
	Entries   []CacheEntry `json:"entries"`
	TotalSize int64        `json:"total_size"`
}

func runCache(cfg daemon.Config, args []string) int {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "help", "-h", "--help":
		fmt.Print(cacheUsage)
		return 0
	case "list", "ls":
		return runCacheList(cfg, args)
	case "prune":
		return runCachePrune(cfg, args)
	default:
		if sub == "" {
			fmt.Fprint(os.Stderr, cacheUsage)
			return 2
		}
		return fail("unknown cache command %q\n%s", sub, cacheUsage)
	}
}

func runCacheList(cfg daemon.Config, args []string) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		default:
			return fail("unknown cache list option %q", a)
		}
	}

	cacheDir := filepath.Join(cfg.Home, "cache")
	entries, totalSize, err := scanCacheDir(cacheDir)
	if err != nil {
		return fail("reading cache: %v", err)
	}

	if asJSON {
		if entries == nil {
			entries = []CacheEntry{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cacheListOutput{Entries: entries, TotalSize: totalSize})
		return 0
	}

	if len(entries) == 0 {
		fmt.Println("consona: cache is empty")
		return 0
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PACKAGE\tVERSION\tCOMMIT\tSIZE\tBUILT\tPATH")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Package,
			e.Version,
			shortCommit(e.Commit),
			formatBytes(e.Size),
			e.Built.Local().Format("2006-01-02 15:04:05"),
			e.Path,
		)
	}
	tw.Flush()
	fmt.Printf("\nTotal size: %s (%d %s)\n", formatBytes(totalSize), len(entries), plural(len(entries), "entry", "entries"))
	return 0
}

func runCachePrune(cfg daemon.Config, args []string) int {
	maxAge := 30 * 24 * time.Hour
	maxAgeStr := "30d"
	dryRun := false

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--older-than":
			if i+1 >= len(args) {
				return fail("--older-than requires a duration (e.g. 7d, 24h, 30m)")
			}
			i++
			d, err := logstore.ParseAge(args[i])
			if err != nil || d <= 0 {
				return fail("invalid duration %q (use e.g. 7d, 24h, 30m)", args[i])
			}
			maxAge = d
			maxAgeStr = args[i]
		case strings.HasPrefix(a, "--older-than="):
			val := strings.TrimPrefix(a, "--older-than=")
			d, err := logstore.ParseAge(val)
			if err != nil || d <= 0 {
				return fail("invalid duration %q (use e.g. 7d, 24h, 30m)", val)
			}
			maxAge = d
			maxAgeStr = val
		default:
			return fail("unknown cache prune option %q", a)
		}
	}

	cacheDir := filepath.Join(cfg.Home, "cache")
	cleanCache := filepath.Clean(cacheDir)
	entries, _, err := scanCacheDir(cacheDir)
	if err != nil {
		return fail("reading cache: %v", err)
	}

	cutoff := time.Now().Add(-maxAge)
	var toPrune []CacheEntry
	var freedSize int64
	for _, e := range entries {
		if e.Built.Before(cutoff) {
			toPrune = append(toPrune, e)
			freedSize += e.Size
		}
	}

	if len(toPrune) == 0 {
		fmt.Printf("consona: cache prune: nothing to remove (no entries older than %s)\n", maxAgeStr)
		return 0
	}

	if dryRun {
		for _, e := range toPrune {
			fmt.Printf("consona: would remove %s@%s (%s, %s, built %s)\n",
				e.Package, e.Version, shortCommit(e.Commit), formatBytes(e.Size), e.Built.Local().Format("2006-01-02 15:04:05"))
		}
		fmt.Printf("consona: cache prune: would free %s (%d %s) (dry run)\n",
			formatBytes(freedSize), len(toPrune), plural(len(toPrune), "entry", "entries"))
		return 0
	}

	for _, e := range toPrune {
		cleanDir := filepath.Clean(e.Dir)
		rel, err := filepath.Rel(cleanCache, cleanDir)
		if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			return fail("safety check failed: path %q is outside cache directory %q", e.Dir, cleanCache)
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 5 {
			return fail("safety check failed: invalid cache directory depth %q", rel)
		}

		if err := os.RemoveAll(cleanDir); err != nil {
			return fail("failed removing %s: %v", cleanDir, err)
		}
		pruneEmptyDirs(cleanCache, filepath.Dir(cleanDir))
		fmt.Printf("consona: removed %s@%s (%s, %s)\n", e.Package, e.Version, shortCommit(e.Commit), formatBytes(e.Size))
	}

	fmt.Printf("consona: cache prune: freed %s (%d %s)\n",
		formatBytes(freedSize), len(toPrune), plural(len(toPrune), "entry", "entries"))

	logstore.Log(logstore.Entry{
		Event:   logstore.EvCachePrune,
		Level:   "info",
		Result:  "ok",
		Message: fmt.Sprintf("pruned %d %s, freed %s", len(toPrune), plural(len(toPrune), "entry", "entries"), formatBytes(freedSize)),
		Fields: map[string]any{
			"freed_bytes": freedSize,
			"entries":     len(toPrune),
			"older_than":  maxAgeStr,
		},
	})

	return 0
}

func scanCacheDir(cacheDir string) ([]CacheEntry, int64, error) {
	if _, err := os.Stat(cacheDir); err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	cleanCache := filepath.Clean(cacheDir)
	var entries []CacheEntry
	var totalSize int64

	err := filepath.WalkDir(cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "src-") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "artifact.json" {
			return nil
		}

		entryDir := filepath.Dir(path)
		cleanEntry := filepath.Clean(entryDir)
		rel, rerr := filepath.Rel(cleanCache, cleanEntry)
		if rerr != nil || strings.HasPrefix(rel, "..") || rel == "." {
			return nil
		}

		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 5 {
			return nil
		}
		_, owner, repo, commit, version := parts[0], parts[1], parts[2], parts[3], parts[4]

		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		var art core.Artifact
		if json.Unmarshal(b, &art) != nil {
			return nil
		}

		tarballName := art.File
		if tarballName == "" {
			tarballName = fmt.Sprintf("%s-%s.tgz", repo, version)
		}
		tarballPath := filepath.Join(cleanEntry, tarballName)
		var size int64
		built := time.Now()
		if st, serr := os.Stat(tarballPath); serr == nil {
			size = st.Size()
			built = st.ModTime()
		} else if st, serr := os.Stat(path); serr == nil {
			size = st.Size()
			built = st.ModTime()
		}

		pkgName := "@" + owner + "/" + repo
		if len(art.Manifest) > 0 {
			var m struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(art.Manifest, &m) == nil && m.Name != "" {
				pkgName = m.Name
			}
		}

		v := art.Version
		if v == "" {
			v = version
		}

		entries = append(entries, CacheEntry{
			Package: pkgName,
			Version: v,
			Commit:  commit,
			Size:    size,
			Built:   built,
			Path:    tarballPath,
			Dir:     cleanEntry,
		})
		totalSize += size
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Built.After(entries[j].Built)
	})

	return entries, totalSize, nil
}

func pruneEmptyDirs(base, dir string) {
	cleanBase := filepath.Clean(base)
	dir = filepath.Clean(dir)
	for dir != cleanBase {
		rel, err := filepath.Rel(cleanBase, dir)
		if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			break
		}
		ents, err := os.ReadDir(dir)
		if err != nil || len(ents) > 0 {
			break
		}
		_ = os.Remove(dir)
		dir = filepath.Dir(dir)
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func shortCommit(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
