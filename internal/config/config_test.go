package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirsAndAtomicWrite(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CONSONA_CONFIG_DIR", filepath.Join(d, "cfg"))
	t.Setenv("CONSONA_LOG_DIR", "")
	if Dir() != filepath.Join(d, "cfg") || LogDir() != filepath.Join(d, "cfg", "logs") {
		t.Fatalf("dirs: %s %s", Dir(), LogDir())
	}
	p := Path("x.json")
	if err := WriteFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatalf("content %q", b)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		dt, _ := os.Stat(Dir())
		if st.Mode().Perm() != 0o600 || dt.Mode().Perm() != 0o700 {
			t.Fatalf("modes %v %v", st.Mode(), dt.Mode())
		}
	}
	es, _ := os.ReadDir(Dir())
	if len(es) != 1 {
		t.Fatalf("temp files left: %v", es)
	}
	t.Setenv("CONSONA_LOG_DIR", filepath.Join(d, "elsewhere"))
	if LogDir() != filepath.Join(d, "elsewhere") {
		t.Fatal(LogDir())
	}
}

func TestPrintKV(t *testing.T) {
	var b bytes.Buffer
	PrintKV(&b, [2]string{"a", "1"}, [2]string{"long key", "2"})
	if b.String() != "a:        1\nlong key: 2\n" {
		t.Fatalf("%q", b.String())
	}
}
