package npm

import (
	"strings"
	"testing"
)

// `--scope @acme`, `--scope acme` and even `@@acme` all produce exactly one
// '@' in the npmrc scope line.
func TestUpdateNpmrcScopeHasSingleAt(t *testing.T) {
	for _, in := range []string{"acme", "@acme", "@@acme", " @Acme "} {
		out, err := UpdateNpmrc("", "http://127.0.0.1:48731/npm/", []string{in})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "@@") || !strings.Contains(out, "\n@acme:registry=http://127.0.0.1:48731/npm/") && !strings.HasPrefix(out, "@acme:registry=") {
			t.Fatalf("%q ->\n%s", in, out)
		}
	}
}

// A previously written "@@x:registry" line (from the old bug) is recognised
// as consona's and rewritten to "@x".
func TestUpdateNpmrcRepairsDoubleAt(t *testing.T) {
	old := "# >>> consona (managed block, do not edit) >>>\n@@acme:registry=http://127.0.0.1:48731/npm/\n# <<< consona <<<\n"
	if k, key, _ := lineKind("@@acme:registry=http://127.0.0.1:48731/npm/"); k != "scope" || key != "acme" {
		t.Fatalf("lineKind: %s %s", k, key)
	}
	out, err := UpdateNpmrc(old, "http://127.0.0.1:48731/npm/", []string{"acme"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "@@") || strings.Count(out, "@acme:registry=") != 1 {
		t.Fatalf("\n%s", out)
	}
}
