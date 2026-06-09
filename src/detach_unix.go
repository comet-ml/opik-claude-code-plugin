//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the command in its own session so SIGHUP from the
// parent shell (or claude exiting) doesn't propagate to it. This lets
// the context-fetch child outlive the hook that spawned it.
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
