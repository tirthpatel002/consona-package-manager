package localcli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileAtomicKeepsModeAndRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".npmrc")
	if err := os.WriteFile(p, []byte("//registry/:_authToken=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode widened to %v", fi.Mode().Perm())
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(p, link); err == nil {
		if err := writeFileAtomic(link, []byte("y")); err == nil {
			t.Fatal("symlink replaced")
		}
	}
}

func TestManagedBlockKeepsOtherLinesAndDetectsClash(t *testing.T) {
	old := "foo=bar\n@acme:registry=https://example.test/\n"
	if got := foreignScopeLines(old, []string{"acme", "other"}); len(got) != 1 || got[0] != "@acme:registry" {
		t.Fatalf("clash not detected: %v", got)
	}
	blk := rcStart + "\n@acme:registry=http://127.0.0.1:1/npm/s/x/\n" + rcEnd + "\n"
	once := replaceBlock("foo=bar\n", blk)
	twice := replaceBlock(once, blk)
	if once != twice || !strings.HasPrefix(once, "foo=bar\n") {
		t.Fatalf("not idempotent / lost lines: %q vs %q", once, twice)
	}
	if got := foreignScopeLines(once, []string{"acme"}); len(got) != 0 {
		t.Fatalf("own block counted as clash: %v", got)
	}
}
