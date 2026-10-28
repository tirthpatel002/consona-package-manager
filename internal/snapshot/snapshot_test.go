package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

const (
	cA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func valid() string {
	return `{"version":1,"scopes":["acme"],"repos":{"acme/ui":{"head":"` + cA + `","tags":{"v1.0.0":"` + cB + `"}}}}`
}

func TestParseRejects(t *testing.T) {
	bad := map[string]string{
		"unknown field":  strings.Replace(valid(), `"scopes"`, `"extra":1,"scopes"`, 1),
		"trailing":       valid() + `{}`,
		"version":        strings.Replace(valid(), `"version":1`, `"version":2`, 1),
		"no repos":       `{"version":1,"scopes":["a"],"repos":{}}`,
		"no scopes":      strings.Replace(valid(), `["acme"]`, `[]`, 1),
		"bad scope":      strings.Replace(valid(), `"acme"]`, `"@acme"]`, 1),
		"uppercase":      strings.Replace(valid(), `acme/ui`, `Acme/ui`, 1),
		"traversal":      strings.Replace(valid(), `acme/ui`, `../ui`, 1),
		"short head":     strings.Replace(valid(), cA, "abc123", 1),
		"short tag sha":  strings.Replace(valid(), cB, "abc", 1),
		"tag with space": strings.Replace(valid(), `v1.0.0`, `v1 0`, 1),
		"dash tag":       strings.Replace(valid(), `v1.0.0`, `-rf`, 1),
		"huge":           valid() + strings.Repeat(" ", MaxBytes),
	}
	for n, in := range bad {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: accepted", n)
		}
	}
	if _, err := Parse([]byte(valid())); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLoadVerifiesHash(t *testing.T) {
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())
	h, err := Save([]byte(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(h); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "snapshot use") {
		t.Fatalf("unloaded id error should say how to recover: %v", err)
	}
	for _, bad := range []string{"../x", h[:10], strings.ToUpper(h)} {
		if _, err := Load(bad); err == nil {
			t.Errorf("Load(%q) accepted", bad)
		}
	}
	// Tampering with the stored file is detected, never served.
	if err := os.WriteFile(filepath.Join(Dir(), h+".json"), []byte(strings.Replace(valid(), cB, cA, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(h); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("tampered snapshot not detected: %v", err)
	}
	if _, err := Save([]byte(`{"version":1}`)); err == nil {
		t.Fatal("invalid snapshot saved")
	}
}

func TestReadFileRefusesSymlinkAndDirs(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	os.WriteFile(real, []byte(valid()), 0o644)
	if _, err := ReadFile(real); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir); err == nil {
		t.Fatal("directory accepted")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
	}
	if _, err := ReadFile(link); err == nil {
		t.Fatal("symlink followed")
	}
}

func TestTagsAndHeadDeterministicAndFailsClosed(t *testing.T) {
	s, _ := Parse([]byte(strings.Replace(valid(), `"v1.0.0":"`+cB+`"`, `"v1.0.0":"`+cB+`","1.0.0":"`+cA+`"`, 1)))
	var first string
	for i := 0; i < 20; i++ {
		revs := core.RevisionsFromTags(mustTags(t, s))
		if first == "" {
			first = revs[0].Commit
		}
		if revs[0].Commit != first {
			t.Fatal("duplicate tag spellings resolved differently between calls")
		}
	}
	_, _, err := s.TagsAndHead(core.Ref{Owner: "acme", Name: "other"})
	if !errors.Is(err, ErrNotInSnapshot) || errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing repo must be ErrNotInSnapshot and not ErrNotFound: %v", err)
	}
	if _, _, err := s.TagsAndHead(core.Ref{Owner: "ACME", Name: "UI"}); err != nil {
		t.Fatalf("case-insensitive lookup: %v", err)
	}
}

func mustTags(t *testing.T, s *Snapshot) ([]core.Tag, string) {
	tags, head, err := s.TagsAndHead(core.Ref{Owner: "acme", Name: "ui"})
	if err != nil {
		t.Fatal(err)
	}
	return tags, head
}

func TestCaptureStability(t *testing.T) {
	ctx := context.Background()
	calls := 0
	stable := func(context.Context) ([]core.Tag, string, error) {
		calls++
		return []core.Tag{{Name: "v1.0.0", Commit: cB}}, cA, nil
	}
	r, err := Capture(ctx, stable, 3, nil)
	if err != nil || calls != 2 || r.Head != cA || r.Tags["v1.0.0"] != cB {
		t.Fatalf("stable: %+v %v calls=%d", r, err, calls)
	}
	// A push lands between the first two reads: detected, retried, accepted once quiet.
	calls = 0
	waits := 0
	moving := func(context.Context) ([]core.Tag, string, error) {
		calls++
		if calls == 2 {
			return []core.Tag{{Name: "v1.0.0", Commit: cB}}, cB, nil // head moved
		}
		return []core.Tag{{Name: "v1.0.0", Commit: cB}}, cA, nil
	}
	r, err = Capture(ctx, moving, 3, func(int) { waits++ })
	if err != nil || r.Head != cA || waits != 1 {
		t.Fatalf("retry: %+v %v waits=%d", r, err, waits)
	}
	// Never quiet: bounded, then an error and no snapshot.
	n := 0
	churn := func(context.Context) ([]core.Tag, string, error) {
		n++
		if n%2 == 0 {
			return nil, cB, nil
		}
		return nil, cA, nil
	}
	if _, err = Capture(ctx, churn, 3, nil); err == nil || n != 6 {
		t.Fatalf("churn: err=%v reads=%d", err, n)
	}
	// A tag that moved between reads is also caught.
	k := 0
	tagMove := func(context.Context) ([]core.Tag, string, error) {
		k++
		c := cB
		if k == 2 {
			c = cA
		}
		return []core.Tag{{Name: "v1.0.0", Commit: c}}, cA, nil
	}
	if r, err = Capture(ctx, tagMove, 3, nil); err != nil || r.Tags["v1.0.0"] != cB {
		t.Fatalf("tag move: %+v %v", r, err)
	}
	// A fetch error is returned as is (no partial snapshot).
	boom := errors.New("boom")
	if _, err = Capture(ctx, func(context.Context) ([]core.Tag, string, error) { return nil, "", boom }, 3, nil); !errors.Is(err, boom) {
		t.Fatalf("fetch error: %v", err)
	}
}
