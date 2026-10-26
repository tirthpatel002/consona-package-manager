package identity

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestInitLoadWhoami(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("CONSONA_CONFIG_DIR", dir)
	if _, err := Load(); err != ErrNotInitialized {
		t.Fatalf("want ErrNotInitialized, got %v", err)
	}
	id, created, err := Init("@Tirth", false)
	if err != nil || !created {
		t.Fatalf("init: %v %v", created, err)
	}
	if id.Username != "tirth" || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id.Device.ID) {
		t.Fatalf("bad identity %+v", id)
	}
	if id.Device.OS != runtime.GOOS || id.Device.Arch != runtime.GOARCH || id.Device.Created.IsZero() {
		t.Fatalf("bad device %+v", id.Device)
	}
	got, err := Load()
	if err != nil || got.Device.ID != id.Device.ID {
		t.Fatalf("load: %+v %v", got, err)
	}
	// Same username again: no-op.
	again, created, err := Init("tirth", false)
	if err != nil || created || again.Device.ID != id.Device.ID {
		t.Fatalf("re-init: %v %v", created, err)
	}
	// Different username needs --force; device id kept.
	if _, _, err := Init("other", false); err == nil {
		t.Fatal("rename without force must fail")
	}
	renamed, _, err := Init("other", true)
	if err != nil || renamed.Username != "other" || renamed.Device.ID != id.Device.ID {
		t.Fatalf("force rename: %+v %v", renamed, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(dir)
		fi, _ := os.Stat(Path())
		if st.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
			t.Fatalf("perms dir %v file %v", st.Mode().Perm(), fi.Mode().Perm())
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	for in, ok := range map[string]bool{"tirth": true, "@T-P.2": true, "a": true, "-x": false, "": false, "a b": false, "a/b": false, "_x": false} {
		if _, err := NormalizeUsername(in); (err == nil) != ok {
			t.Errorf("%q: err=%v want ok=%v", in, err, ok)
		}
	}
}

func TestLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	unlock, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock2, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
}
