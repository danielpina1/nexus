//go:build unix

package node

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes cmd the leader of a new process group and makes
// context cancellation kill the whole group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
