package devsync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// FileName is the tracking file stored in CONSONA_HOME.
const FileName = "dev-projects.json"

// MaxProjectsPerPackage caps how many project directories are remembered per package.
const MaxProjectsPerPackage = 50

// ProjectEntry records a project directory where a package tarball was fetched.
type ProjectEntry struct {
	Dir      string    `json:"dir"`
	LastSeen time.Time `json:"last_seen"`
}

var trackMu sync.Mutex

func devProjectsPath(home string) string {
	if home == "" {
		home = core.Home()
	}
	return filepath.Join(home, FileName)
}

// TrackProject records the calling npm project directory for a local-source package.
// It enforces dedup, cap (50), absolute existing directory check, atomic write, and logs "dev.project_tracked".
func TrackProject(home, pkgName, dir string) error {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return nil
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil
	}

	pkgName = strings.ToLower(strings.TrimSpace(pkgName))
	if pkgName == "" {
		return nil
	}

	trackMu.Lock()
	defer trackMu.Unlock()

	p := devProjectsPath(home)
	data, err := readProjectsFile(p)
	if err != nil {
		data = make(map[string][]ProjectEntry)
	}

	now := time.Now().UTC()
	list := data[pkgName]
	found := false
	for i := range list {
		if list[i].Dir == dir {
			list[i].LastSeen = now
			found = true
			break
		}
	}
	if !found {
		list = append(list, ProjectEntry{Dir: dir, LastSeen: now})
	}

	// Sort newest first
	sort.Slice(list, func(i, j int) bool {
		return list[i].LastSeen.After(list[j].LastSeen)
	})

	if len(list) > MaxProjectsPerPackage {
		list = list[:MaxProjectsPerPackage]
	}
	data[pkgName] = list

	bs, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	bs = append(bs, '\n')
	if err := config.WriteFileAtomic(p, bs, 0o600); err != nil {
		return err
	}

	logstore.LogQuiet(logstore.Entry{
		Event:     logstore.EvDevProjectTracked,
		Level:     "debug",
		Ecosystem: "npm",
		Package:   pkgName,
		Result:    "ok",
		Fields:    map[string]any{"dir": dir},
	})
	return nil
}

// GetTrackedProjects returns tracked projects for pkgName.
func GetTrackedProjects(home, pkgName string) ([]ProjectEntry, error) {
	trackMu.Lock()
	defer trackMu.Unlock()

	pkgName = strings.ToLower(strings.TrimSpace(pkgName))
	data, err := readProjectsFile(devProjectsPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return data[pkgName], nil
}

// GetAllTrackedProjects returns the entire map of tracked packages and projects.
func GetAllTrackedProjects(home string) (map[string][]ProjectEntry, error) {
	trackMu.Lock()
	defer trackMu.Unlock()

	data, err := readProjectsFile(devProjectsPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string][]ProjectEntry), nil
		}
		return nil, err
	}
	return data, nil
}

func readProjectsFile(path string) (map[string][]ProjectEntry, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string][]ProjectEntry
	if err := json.Unmarshal(bs, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = make(map[string][]ProjectEntry)
	}
	return out, nil
}
