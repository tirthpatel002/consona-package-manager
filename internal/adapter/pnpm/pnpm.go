// Package pnpm is the pnpm adapter: it integrates pnpm with the local consona
// daemon, serving packages using the same registry configuration and npmrc
// written by consona setup, warming private packages on install/add, and
// filtering private names from audits.
package pnpm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"

	npmadapter "github.com/tirthpatel002/consona-package-manager/internal/adapter/npm"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

func init() { core.Register(Adapter{}) }

// Adapter implements core.Adapter for pnpm.
type Adapter struct {
	npmadapter.Adapter
}

func (Adapter) Name() string { return "pnpm" }

// Exec runs pnpm with args, passing stdio and the exit code through.
// On install, i, and add, the daemon warms private dependencies in parallel.
func (Adapter) Exec(ctx context.Context, baseURL string, args []string) int {
	if len(args) > 0 && (args[0] == "install" || args[0] == "i" || args[0] == "add") {
		go npmadapter.Warm(baseURL, npmadapter.WarmNames(".", args, npmadapter.RoutedScopes()))
	}
	return run(ctx, args)
}

func run(ctx context.Context, args []string) int {
	cmd := exec.CommandContext(ctx, "pnpm", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		fmt.Fprintln(os.Stderr, "consona: cannot run pnpm:", err)
		return 127
	}
}
