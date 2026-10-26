package pnpm_test

import (
	"testing"

	_ "github.com/tirthpatel002/consona-package-manager/internal/adapter/pnpm"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

func TestPnpmAdapterRegistered(t *testing.T) {
	a, ok := core.Lookup("pnpm")
	if !ok {
		t.Fatal("pnpm adapter not registered in core")
	}
	if a.Name() != "pnpm" {
		t.Errorf("got name %q; want %q", a.Name(), "pnpm")
	}
}
