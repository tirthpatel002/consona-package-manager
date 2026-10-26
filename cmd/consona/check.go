package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	npmadapter "github.com/tirthpatel002/consona-package-manager/internal/adapter/npm"
	"github.com/tirthpatel002/consona-package-manager/internal/localcli"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/lookalike"
)

type checkResultItem struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "lookalike", "ok", "skipped"
	Detail string `json:"detail,omitempty"`
}

type checkSummaryJSON struct {
	Packages   []checkResultItem `json:"packages"`
	Lookalikes int               `json:"lookalikes"`
	Total      int               `json:"total"`
}

func runCheck(args []string) int {
	return runCheckArgs(args, os.Stdout, os.Stderr)
}

func runCheckArgs(args []string, stdout, stderr io.Writer) int {
	var jsonOutput bool
	var dir string

	for _, a := range args {
		switch {
		case a == "--json":
			jsonOutput = true
		case a == "--help" || a == "-h":
			fmt.Fprintln(stdout, "Usage: consona check [dir] [--json]")
			fmt.Fprintln(stdout, "Check private dependencies in package.json and package-lock.json for public lookalikes.")
			return 0
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "consona: unknown flag %q\n", a)
			return 2
		default:
			if dir != "" {
				fmt.Fprintf(stderr, "consona: too many arguments; expected [dir]\n")
				return 2
			}
			dir = a
		}
	}

	if dir == "" {
		dir = "."
	}

	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "consona: directory %q not found\n", dir)
		return 2
	}

	items, err := inspectAndCheck(context.Background(), dir)
	if err != nil {
		fmt.Fprintf(stderr, "consona: %v\n", err)
		return 2
	}

	lookalikes := 0
	for _, item := range items {
		if item.Status == "lookalike" {
			lookalikes++
		}
	}

	if jsonOutput {
		if items == nil {
			items = []checkResultItem{}
		}
		summary := checkSummaryJSON{
			Packages:   items,
			Lookalikes: lookalikes,
			Total:      len(items),
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
	} else {
		if len(items) == 0 {
			fmt.Fprintf(stdout, "consona: 0 private packages found in %s\n", dir)
		} else {
			for _, item := range items {
				switch item.Status {
				case "lookalike":
					fmt.Fprintf(stdout, "%s  LOOKALIKE  %s\n", item.Name, item.Detail)
				case "ok":
					fmt.Fprintf(stdout, "%s  ok\n", item.Name)
				default:
					if item.Detail != "" {
						fmt.Fprintf(stdout, "%s  skipped (%s)\n", item.Name, item.Detail)
					} else {
						fmt.Fprintf(stdout, "%s  skipped\n", item.Name)
					}
				}
			}
			if lookalikes > 0 {
				fmt.Fprintf(stdout, "consona: %d lookalike(s) found across %d private packages\n", lookalikes, len(items))
			} else {
				fmt.Fprintf(stdout, "consona: 0 lookalikes found across %d private packages\n", len(items))
			}
		}
	}

	if lookalikes > 0 {
		return 1
	}
	return 0
}

func inspectAndCheck(ctx context.Context, dir string) ([]checkResultItem, error) {
	pkgPath := filepath.Join(dir, "package.json")
	rawPkg, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", pkgPath, err)
	}

	var pj struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(rawPkg, &pj); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", pkgPath, err)
	}

	allDeps := make(map[string]bool)
	addDeps := func(m map[string]string) {
		for k := range m {
			allDeps[k] = true
		}
	}
	addDeps(pj.Dependencies)
	addDeps(pj.DevDependencies)
	addDeps(pj.OptionalDependencies)
	addDeps(pj.PeerDependencies)

	lockPath := filepath.Join(dir, "package-lock.json")
	if rawLock, err := os.ReadFile(lockPath); err == nil {
		for _, name := range parseLockfileDeps(rawLock) {
			allDeps[name] = true
		}
	}

	scopes := npmadapter.PrivateScopes()
	for _, o := range localcli.ExtraOwners() {
		scopes[strings.ToLower(o)] = true
	}

	var privateDeps []string
	for dep := range allDeps {
		if isPrivateScoped(dep, scopes) {
			privateDeps = append(privateDeps, dep)
		}
	}
	sort.Strings(privateDeps)

	if len(privateDeps) == 0 {
		return nil, nil
	}

	regHost := lookalike.RegistryHost(lookalike.PublicRegistry())
	say("checking %d private packages against %s...", len(privateDeps), regHost)

	checker := lookalike.Default()
	var results []checkResultItem

	for _, dep := range privateDeps {
		say("checking %s...", dep)
		st := checker.Check(ctx, dep)
		switch st {
		case lookalike.StatusExists:
			results = append(results, checkResultItem{
				Name:   dep,
				Status: "lookalike",
				Detail: "public registry has this name",
			})
			logstore.Log(logstore.Entry{
				Event:     logstore.EvPublicLookalike,
				Level:     "warn",
				Ecosystem: "npm",
				Package:   dep,
				Result:    "lookalike",
				Message:   fmt.Sprintf("%s also exists on the public npm registry (%s)", dep, regHost),
			})
		case lookalike.StatusNotExists:
			results = append(results, checkResultItem{
				Name:   dep,
				Status: "ok",
			})
		default:
			results = append(results, checkResultItem{
				Name:   dep,
				Status: "skipped",
				Detail: "public registry check skipped",
			})
		}
	}

	return results, nil
}

func isPrivateScoped(name string, scopes map[string]bool) bool {
	if !strings.HasPrefix(name, "@") {
		return false
	}
	parts := strings.Split(name[1:], "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	return scopes[strings.ToLower(parts[0])]
}

func parseLockfileDeps(raw []byte) []string {
	var lock struct {
		Packages     map[string]json.RawMessage `json:"packages"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil
	}

	var names []string

	// v2 / v3 packages map
	for key, rawItem := range lock.Packages {
		if key == "" {
			continue
		}
		var item struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(rawItem, &item); err == nil && item.Name != "" {
			names = append(names, item.Name)
			continue
		}
		// Fallback to path extraction: node_modules/@scope/pkg or node_modules/pkg
		if idx := strings.LastIndex(key, "node_modules/"); idx >= 0 {
			names = append(names, key[idx+len("node_modules/"):])
		}
	}

	// v1 / v2 dependencies map
	extractV1Dependencies(lock.Dependencies, &names)

	return names
}

func extractV1Dependencies(deps map[string]json.RawMessage, out *[]string) {
	for name, rawItem := range deps {
		*out = append(*out, name)
		var item struct {
			Dependencies map[string]json.RawMessage `json:"dependencies"`
		}
		if err := json.Unmarshal(rawItem, &item); err == nil && len(item.Dependencies) > 0 {
			extractV1Dependencies(item.Dependencies, out)
		}
	}
}
