package npmpack

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildDeterministicAndRespectsFiles(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	src := map[string]string{
		"package.json": `{"name":"wrong-name","version":"0.1.0","main":"index.js","files":["index.js"]}`,
		"index.js":     "module.exports = 1\n",
		"secret.txt":   "not packed\n",
		"README.md":    "# always packed\n",
	}
	var arts []*core.Artifact
	for i := 0; i < 2; i++ {
		dir, out := t.TempDir(), t.TempDir()
		writeTree(t, dir, src)
		a, err := Builder{}.Build(context.Background(), dir, core.BuildRequest{Name: "@o/pkg", Version: "1.2.3", OutDir: out})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(out, a.File))
		if err != nil {
			t.Fatal(err)
		}
		s1, s512 := Hashes(data)
		if s1 != a.SHA1 || s512 != a.SHA512 {
			t.Fatal("artifact hashes do not match tarball bytes")
		}
		if i == 0 {
			if got := tarNames(t, data); !equal(got, []string{"package/README.md", "package/index.js", "package/package.json"}) {
				t.Fatalf("packed files %v", got)
			}
		}
		arts = append(arts, a)
	}
	if arts[0].File != "o-pkg-1.2.3.tgz" || arts[0].SHA512 != arts[1].SHA512 {
		t.Fatalf("not deterministic: %+v vs %+v", arts[0], arts[1])
	}
}

func tarNames(t *testing.T, data []byte) []string {
	f, _ := os.CreateTemp(t.TempDir(), "x")
	f.Write(data)
	f.Seek(0, 0)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A prepare script runs before packing (like npm does for git deps), so a
// `files: ["dist"]` package ships its build output; a failing script is a
// BuildError carrying the output.
func TestBuildRunsPrepare(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	dir, out := t.TempDir(), t.TempDir()
	writeTree(t, dir, map[string]string{
		"package.json": `{"name":"@o/ts","version":"1.0.0","main":"dist/index.js","files":["dist"],` +
			`"scripts":{"prepare":"node -e \"require('fs').mkdirSync('dist',{recursive:true});require('fs').writeFileSync('dist/index.js','module.exports=42')\""}}`,
		"src/index.ts": "export default 42\n",
	})
	info, err := Builder{}.Inspect(dir)
	if err != nil || !info.NeedsBuild {
		t.Fatalf("inspect: %v %+v", err, info)
	}
	a, err := Builder{}.Build(context.Background(), dir, core.BuildRequest{Name: "@o/ts", Version: "1.0.0", OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(out, a.File))
	if got := tarNames(t, data); !equal(got, []string{"package/dist/index.js", "package/package.json"}) {
		t.Fatalf("packed files %v", got)
	}

	bad := t.TempDir()
	writeTree(t, bad, map[string]string{"package.json": `{"name":"@o/bad","version":"1.0.0","scripts":{"prepare":"echo boom-output && exit 3"}}`})
	_, err = Builder{}.Build(context.Background(), bad, core.BuildRequest{Name: "@o/bad", Version: "1.0.0", OutDir: t.TempDir()})
	var be *core.BuildError
	if !errors.As(err, &be) || !strings.Contains(string(be.Output), "boom-output") {
		t.Fatalf("want BuildError with output, got %v", err)
	}
}
