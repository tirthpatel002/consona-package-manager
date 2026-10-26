package daemon

import (
	"os/exec"
	"syscall"
)

// detach runs the daemon without a console so it outlives the CLI.
func detach(cmd *exec.Cmd) {
	const detachedProcess, newProcessGroup = 0x00000008, 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | newProcessGroup}
}
