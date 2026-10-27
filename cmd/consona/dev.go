package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/devsync"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
)

const devUsage = `usage: consona dev <@scope/pkg> [--project DIR ...] [--interval 500ms] [--once]
       consona dev --list
`

func cmdDev(args []string) int {
	var listFlag bool
	var onceFlag bool
	var intervalFlag = devsync.DefaultPollInterval
	var projectFlags []string
	var pkgName string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--list":
			listFlag = true
		case a == "--once":
			onceFlag = true
		case a == "--project":
			if i+1 >= len(args) {
				return fail("missing directory after --project")
			}
			i++
			projectFlags = append(projectFlags, args[i])
		case strings.HasPrefix(a, "--project="):
			projectFlags = append(projectFlags, strings.TrimPrefix(a, "--project="))
		case a == "--interval":
			if i+1 >= len(args) {
				return fail("missing duration after --interval")
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return fail("invalid --interval %q: %v", args[i], err)
			}
			intervalFlag = d
		case strings.HasPrefix(a, "--interval="):
			val := strings.TrimPrefix(a, "--interval=")
			d, err := time.ParseDuration(val)
			if err != nil {
				return fail("invalid --interval %q: %v", val, err)
			}
			intervalFlag = d
		case a == "-h" || a == "--help":
			fmt.Fprint(os.Stderr, devUsage)
			return 0
		case strings.HasPrefix(a, "-"):
			return fail("unknown flag %q", a)
		default:
			if pkgName == "" {
				pkgName = a
			} else {
				return fail("unexpected argument %q", a)
			}
		}
	}

	if listFlag {
		return runDevList()
	}

	if pkgName == "" {
		fmt.Fprint(os.Stderr, devUsage)
		return 2
	}

	entry, ok, err := localindex.Open().Lookup(pkgName)
	if err != nil {
		return fail("looking up %s: %v", pkgName, err)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "consona: %s is not registered on this device (register it with `consona register <folder>`)\n", pkgName)
		return 1
	}

	var targets []string
	if len(projectFlags) > 0 {
		seen := make(map[string]bool)
		for _, p := range projectFlags {
			abs, err := filepath.Abs(p)
			if err != nil {
				return fail("invalid project path %q: %v", p, err)
			}
			st, err := os.Stat(abs)
			if err != nil || !st.IsDir() {
				return fail("project directory does not exist: %s", abs)
			}
			if !seen[abs] {
				seen[abs] = true
				targets = append(targets, abs)
			}
		}
	} else {
		tracked, err := devsync.GetTrackedProjects(core.Home(), entry.Name)
		if err != nil {
			return fail("reading tracked projects: %v", err)
		}
		for _, tp := range tracked {
			nmPkg := filepath.Join(tp.Dir, "node_modules", filepath.FromSlash(entry.Name))
			if st, err := os.Stat(nmPkg); err == nil && st.IsDir() {
				targets = append(targets, tp.Dir)
			}
		}
		if len(targets) == 0 {
			fmt.Fprintf(os.Stderr, "consona: dev: no target projects found for %s (install it in a project first or specify --project <dir>)\n", entry.Name)
			return 1
		}
	}

	syncOnce := func() bool {
		tmpOut, err := os.MkdirTemp("", "consona-dev-tar-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "consona: dev: failed to create temporary build directory: %v\n", err)
			return false
		}
		defer os.RemoveAll(tmpOut)

		tarPath, err := devsync.BuildAndPack(context.Background(), entry.Path, entry.Name, tmpOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "consona: dev: build failed for %s: %v\n", entry.Name, err)
			return false
		}

		allOK := true
		for _, target := range targets {
			res, err := devsync.SyncTarballToProject(tarPath, entry.Name, target)
			if err != nil {
				allOK = false
				fmt.Fprintf(os.Stderr, "consona: dev: skipping %s: %v\n", target, err)
				continue
			}
			say("dev: synced %s (%d files, %d changed) -> %s in %dms",
				entry.Name, res.TotalFiles, res.ChangedFiles, res.TargetDir, res.Duration.Milliseconds())
		}
		return allOK
	}

	initialOK := syncOnce()
	if onceFlag {
		if !initialOK {
			return 1
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	say("dev: watching %s (polling every %s) -> %d project(s)", entry.Path, intervalFlag, len(targets))

	err = devsync.Watch(ctx, devsync.WatchConfig{
		Dir:           entry.Path,
		PollInterval:  intervalFlag,
		DebounceQuiet: devsync.DefaultDebounceQuiet,
		OnChange: func() {
			syncOnce()
		},
	})
	if err != nil && err != context.Canceled {
		return fail("dev watcher error: %v", err)
	}

	say("dev: stopped watching %s", entry.Name)
	return 0
}

func runDevList() int {
	entries, err := localindex.Open().List()
	if err != nil {
		return fail("listing registered packages: %v", err)
	}
	if len(entries) == 0 {
		fmt.Println("consona: dev: nothing registered on this device (consona register <folder>)")
		return 0
	}

	allTracked, err := devsync.GetAllTrackedProjects(core.Home())
	if err != nil {
		return fail("reading tracked projects: %v", err)
	}

	fmt.Println("consona dev: registered local packages:")
	for _, e := range entries {
		fmt.Printf("%s (%s):\n", e.Name, e.Path)
		tracked := allTracked[strings.ToLower(e.Name)]
		if len(tracked) == 0 {
			fmt.Println("  (no tracked projects)")
			continue
		}
		for _, tp := range tracked {
			nmPkg := filepath.Join(tp.Dir, "node_modules", filepath.FromSlash(e.Name))
			status := "missing"
			if st, err := os.Stat(nmPkg); err == nil && st.IsDir() {
				status = "node_modules present"
			}
			fmt.Printf("  %s (%s)\n", tp.Dir, status)
		}
	}
	return 0
}
