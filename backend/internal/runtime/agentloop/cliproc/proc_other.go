//go:build !unix

package cliproc

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

// Without process groups only the direct child is killed.
func terminateGroup(cmd *exec.Cmd) { killGroup(cmd) }

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
