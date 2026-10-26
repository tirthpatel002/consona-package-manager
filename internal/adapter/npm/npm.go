// Package npm is the npm adapter: it points npm's registry (and the private
// scopes) in the user's npmrc at the local daemon, serves the private scopes
// itself, redirects everything public to the upstream registry, filters
// private names out of audits, and wraps the npm command.
package npm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

func init() { core.Register(Adapter{}) }

// Adapter implements core.Adapter for npm.
type Adapter struct{}

func (Adapter) Name() string { return "npm" }

// Exec runs npm with args, passing stdio and the exit code through. While
// npm boots, the daemon resolves the project's private packages in
// parallel. Audit needs nothing special: npm's registry is the daemon,
// which filters private names out of audit requests.
func (Adapter) Exec(ctx context.Context, baseURL string, args []string) int {
	if len(args) > 0 && (args[0] == "install" || args[0] == "i") {
		go warm(baseURL, WarmNames(".", args, RoutedScopes()))
	}
	return run(ctx, args)
}

func run(ctx context.Context, args []string) int {
	cmd := exec.CommandContext(ctx, "npm", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The terminal delivers Ctrl-C to npm too; we just wait for it.
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
		fmt.Fprintln(os.Stderr, "consona: cannot run npm:", err)
		return 127
	}
}
