//go:build !unix

package node

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
