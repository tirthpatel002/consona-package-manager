package redact

import (
	"strings"
	"testing"
)

func TestStringHidesTokens(t *testing.T) {
	fine := "github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
	classic := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	for _, in := range []string{
		"token " + classic,
		"Authorization: Bearer " + fine,
		"url?x=" + fine + "&y",
		"//r/:_authToken=" + classic,
		"gho_" + strings.Repeat("a", 36),
	} {
		out := String(in)
		if strings.Contains(out, fine) || strings.Contains(out, classic) || strings.Contains(out, strings.Repeat("a", 36)) {
			t.Errorf("not redacted: %q -> %q", in, out)
		}
	}
	AddSecret("plain-secret-without-prefix")
	if out := String("x plain-secret-without-prefix y"); strings.Contains(out, "plain-secret") {
		t.Errorf("registered secret leaked: %q", out)
	}
	if out := String("@acme/lib 404 not found"); out != "@acme/lib 404 not found" {
		t.Errorf("ordinary text changed: %q", out)
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDwxyz": "github_pat_…wxyz",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789":                          "ghp_…6789",
		"short":                "…",
		"0123456789abcdefXYZW": "…XYZW",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
