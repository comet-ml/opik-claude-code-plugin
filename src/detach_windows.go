//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detachProcess flags the child as a new process group so closing the
// parent's console window doesn't kill it via Ctrl-C/Ctrl-Break events.
// Equivalent to Setsid on Unix — keeps the context-fetch subprocess alive
// past the hook's exit.
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
