package lifecycle

import (
	"context"
	"os/exec"
)

// CommandRunner executes exactly one allow-listed command per call.
// Real production impl uses OSCommandRunner; tests inject a mock.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// OSCommandRunner is the production CommandRunner; it forks the named binary.
type OSCommandRunner struct{}

// Run forks name with args. name must be an absolute path or a command on PATH.
func (OSCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
