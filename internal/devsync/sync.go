package devsync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/source/local"
)

// ErrSymlinkRefused is returned when target node_modules/<pkg> is a symlink.
var ErrSymlinkRefused = errors.New("target is a symlink")

type manifest struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Scripts map[string]string `json:"scripts"`
}

func readManifest(dir string) (*manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, fmt.Errorf("no package.json at %s: %w", dir, err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("invalid package.json at %s: %w", dir, err)
	}
	if m.Version == "" {
		m.Version = "0.0.0"
	}
	return &m, nil
}

// BuildAndPack builds and packs srcDir into outDir.
// If package.json has prepare/prepack scripts, it runs the build via npmpack.Builder.
// If no build scripts are present and npm is unavailable, it falls back to a pure Go tarball packer.
func BuildAndPack(ctx context.Context, srcDir, pkgName, outDir string) (string, error) {
	m, err := readManifest(srcDir)
	if err != nil {
		return "", err
	}

	tmpBuildDir, err := os.MkdirTemp("", "consona-dev-build-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpBuildDir)

	if err := local.CopyTree(srcDir, tmpBuildDir); err != nil {
		return "", fmt.Errorf("copying source: %w", err)
	}

	hasScripts := m.Scripts["prepare"] != "" || m.Scripts["prepack"] != ""
	hasNPM := false
	if _, err := exec.LookPath("npm"); err == nil {
		hasNPM = true
	}

	if hasScripts && !hasNPM {
		return "", fmt.Errorf("npm is required to build %s with prepare/prepack scripts", pkgName)
	}

	startBuild := time.Now()
	if hasScripts || hasNPM {
		b := npmpack.Builder{}
		art, err := b.Build(ctx, tmpBuildDir, core.BuildRequest{Name: pkgName, Version: m.Version, OutDir: outDir})
		dur := time.Since(startBuild).Milliseconds()
		if err != nil {
			logstore.Log(logstore.Entry{
				Event:      logstore.EvDevBuild,
				Level:      "error",
				Ecosystem:  "npm",
				Package:    pkgName,
				Result:     "fail",
				DurationMS: dur,
				Message:    err.Error(),
			})
			return "", err
		}
		logstore.Log(logstore.Entry{
			Event:      logstore.EvDevBuild,
			Level:      "info",
			Ecosystem:  "npm",
			Package:    pkgName,
			Result:     "ok",
			DurationMS: dur,
		})
		return filepath.Join(outDir, art.File), nil
	}

	// Pure Go packing fallback (when no build scripts and no npm on PATH)
	tarFile := npmpack.FileName(pkgName, m.Version)
	tarPath := filepath.Join(outDir, tarFile)
	if err := packPureGo(tmpBuildDir, tarPath); err != nil {
		logstore.Log(logstore.Entry{
			Event:     logstore.EvDevBuild,
			Level:     "error",
			Ecosystem: "npm",
			Package:   pkgName,
			Result:    "fail",
			Message:   err.Error(),
		})
		return "", err
	}
	logstore.Log(logstore.Entry{
		Event:      logstore.EvDevBuild,
		Level:      "info",
		Ecosystem:  "npm",
		Package:    pkgName,
		Result:     "ok",
		DurationMS: time.Since(startBuild).Milliseconds(),
	})
	return tarPath, nil
}

func packPureGo(srcDir, tarballPath string) error {
	f, err := os.Create(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdrName := "package/" + filepath.ToSlash(rel)
		if fi.IsDir() {
			hdr := &tar.Header{
				Name:     hdrName + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
				ModTime:  fi.ModTime(),
			}
			return tw.WriteHeader(hdr)
		}
		hdr := &tar.Header{
			Name:     hdrName,
			Mode:     int64(fi.Mode().Perm()),
			Size:     fi.Size(),
			Typeflag: tar.TypeReg,
			ModTime:  fi.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
}

// SyncResult holds the counts and elapsed time of a sync operation.
type SyncResult struct {
	TargetDir    string
	TotalFiles   int
	ChangedFiles int
	Duration     time.Duration
}

// SyncTarballToProject extracts the packed tarball into target project's node_modules/<pkg>.
// It enforces path-escape protections, refuses symlinked target node_modules/<pkg>,
// performs an atomic swap, and counts changed files.
func SyncTarballToProject(tarballPath, pkgName, projectDir string) (SyncResult, error) {
	startSync := time.Now()
	targetDir := filepath.Join(projectDir, "node_modules", filepath.FromSlash(pkgName))
	res := SyncResult{TargetDir: targetDir}

	// Refuse if node_modules/<pkg> is itself a symlink
	if fi, err := os.Lstat(targetDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			linkDest, _ := os.Readlink(targetDir)
			err := fmt.Errorf("%w: node_modules/%s is a symlink pointing to %s", ErrSymlinkRefused, pkgName, linkDest)
			logstore.Log(logstore.Entry{
				Event:     logstore.EvDevSync,
				Level:     "warn",
				Ecosystem: "npm",
				Package:   pkgName,
				Result:    "fail",
				ErrorCode: "symlink_refused",
				Message:   err.Error(),
				Fields:    map[string]any{"project": projectDir},
			})
			return res, err
		}
	}

	parentDir := filepath.Dir(targetDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return res, fmt.Errorf("creating parent dir %s: %w", parentDir, err)
	}

	// Temp directory inside node_modules so os.Rename is atomic (same filesystem)
	tmpDir, err := os.MkdirTemp(parentDir, ".tmp-dev-sync-*")
	if err != nil {
		return res, fmt.Errorf("creating temp sync dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarballSafe(tarballPath, tmpDir); err != nil {
		logstore.Log(logstore.Entry{
			Event:     logstore.EvDevSync,
			Level:     "error",
			Ecosystem: "npm",
			Package:   pkgName,
			Result:    "fail",
			ErrorCode: "extract_failed",
			Message:   err.Error(),
			Fields:    map[string]any{"project": projectDir},
		})
		return res, err
	}

	totalFiles, changedFiles := countChanges(tmpDir, targetDir)
	res.TotalFiles = totalFiles
	res.ChangedFiles = changedFiles

	// Atomic swap:
	// If targetDir exists: rename targetDir -> oldTemp, rename tmpDir -> targetDir, remove oldTemp
	if _, err := os.Stat(targetDir); err == nil {
		oldTemp, err := os.MkdirTemp(parentDir, ".tmp-dev-old-*")
		if err != nil {
			return res, fmt.Errorf("creating old temp dir: %w", err)
		}
		_ = os.Remove(oldTemp) // free up path for rename
		if err := os.Rename(targetDir, oldTemp); err != nil {
			return res, fmt.Errorf("renaming existing target dir: %w", err)
		}
		if err := os.Rename(tmpDir, targetDir); err != nil {
			// Try to restore old
			_ = os.Rename(oldTemp, targetDir)
			return res, fmt.Errorf("renaming new target dir: %w", err)
		}
		_ = os.RemoveAll(oldTemp)
	} else {
		if err := os.Rename(tmpDir, targetDir); err != nil {
			return res, fmt.Errorf("moving new target dir in place: %w", err)
		}
	}

	res.Duration = time.Since(startSync)
	logstore.Log(logstore.Entry{
		Event:      logstore.EvDevSync,
		Level:      "info",
		Ecosystem:  "npm",
		Package:    pkgName,
		Result:     "ok",
		DurationMS: res.Duration.Milliseconds(),
		Fields: map[string]any{
			"project":       projectDir,
			"files_total":   totalFiles,
			"files_changed": changedFiles,
		},
	})
	return res, nil
}

func extractTarballSafe(tarballPath, destDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	destDirClean := filepath.Clean(destDir)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		rawName := strings.TrimSpace(hdr.Name)
		if rawName == "" || rawName == "." {
			continue
		}

		// Security: Reject absolute paths
		if filepath.IsAbs(rawName) || strings.HasPrefix(rawName, "/") || strings.HasPrefix(rawName, "\\") {
			return fmt.Errorf("malicious tar entry: absolute path %q", hdr.Name)
		}

		name := rawName
		if strings.HasPrefix(name, "package/") {
			name = strings.TrimPrefix(name, "package/")
		} else if strings.HasPrefix(name, "package\\") {
			name = strings.TrimPrefix(name, "package\\")
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "." {
			continue
		}

		// Security: Reject absolute paths after package prefix strip
		if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
			return fmt.Errorf("malicious tar entry: absolute path %q", hdr.Name)
		}

		// Security: Reject traversal with ..
		cleanRel := filepath.Clean(filepath.FromSlash(name))
		if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("malicious tar entry: path traversal %q", hdr.Name)
		}

		targetPath := filepath.Join(destDirClean, cleanRel)
		relToDest, err := filepath.Rel(destDirClean, targetPath)
		if err != nil || relToDest == ".." || strings.HasPrefix(relToDest, ".."+string(filepath.Separator)) {
			return fmt.Errorf("malicious tar entry: path escapes destination %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := checkNoSymlinkAncestors(destDirClean, targetPath); err != nil {
				return err
			}
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := checkNoSymlinkAncestors(destDirClean, filepath.Dir(targetPath)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			perm := hdr.FileInfo().Mode().Perm()
			if perm == 0 {
				perm = 0o644
			}
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}

		case tar.TypeSymlink:
			// Reject symlinks that escape target dir
			linkTarget := hdr.Linkname
			if filepath.IsAbs(linkTarget) || strings.HasPrefix(linkTarget, "/") || strings.HasPrefix(linkTarget, "\\") {
				return fmt.Errorf("malicious tar entry: symlink %q has absolute target %q", hdr.Name, linkTarget)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(cleanRel), filepath.FromSlash(linkTarget)))
			if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return fmt.Errorf("malicious tar entry: symlink %q escapes root to %q", hdr.Name, linkTarget)
			}
			if err := checkNoSymlinkAncestors(destDirClean, filepath.Dir(targetPath)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			_ = os.Remove(targetPath)
			if err := os.Symlink(linkTarget, targetPath); err != nil {
				return err
			}

		case tar.TypeLink:
			// Hardlink: validate target within package
			linkTarget := hdr.Linkname
			if strings.HasPrefix(linkTarget, "package/") {
				linkTarget = strings.TrimPrefix(linkTarget, "package/")
			}
			cleanLink := filepath.Clean(filepath.FromSlash(linkTarget))
			if cleanLink == ".." || strings.HasPrefix(cleanLink, ".."+string(filepath.Separator)) {
				return fmt.Errorf("malicious tar entry: hardlink %q escapes root to %q", hdr.Name, hdr.Linkname)
			}
			targetLinkPath := filepath.Join(destDirClean, cleanLink)
			if err := checkNoSymlinkAncestors(destDirClean, filepath.Dir(targetPath)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			_ = os.Remove(targetPath)
			if err := os.Link(targetLinkPath, targetPath); err != nil {
				return err
			}

		default:
			// Ignore unsupported entries (FIFO, etc.)
		}
	}
	return nil
}

func checkNoSymlinkAncestors(root, p string) error {
	curr := p
	for {
		if curr == root || curr == "." || curr == "/" {
			break
		}
		fi, err := os.Lstat(curr)
		if err == nil && (fi.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("malicious path: %s is a symlink", curr)
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return nil
}

func countChanges(newDir, oldDir string) (totalFiles int, changedFiles int) {
	newFiles := make(map[string]int64)
	newContents := make(map[string][]byte)

	_ = filepath.Walk(newDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(newDir, p)
		if err != nil {
			return nil
		}
		totalFiles++
		newFiles[rel] = fi.Size()
		data, _ := os.ReadFile(p)
		newContents[rel] = data
		return nil
	})

	if _, err := os.Stat(oldDir); err != nil {
		// Old dir didn't exist: all new files are changed
		return totalFiles, totalFiles
	}

	oldFiles := make(map[string]int64)
	_ = filepath.Walk(oldDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(oldDir, p)
		if err != nil {
			return nil
		}
		oldFiles[rel] = fi.Size()
		data, _ := os.ReadFile(p)
		newData, exists := newContents[rel]
		if !exists || !bytes.Equal(data, newData) {
			changedFiles++
		}
		return nil
	})

	// Check files added in newDir that were not in oldDir
	for rel := range newFiles {
		if _, exists := oldFiles[rel]; !exists {
			changedFiles++
		}
	}

	return totalFiles, changedFiles
}
