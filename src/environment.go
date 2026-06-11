package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// environmentBlockText approximates the dynamic environment block Claude
// Code injects into its system prompt every session: working directory,
// platform/OS, and a git status snapshot with recent commits. We never see
// the real text, but its inputs are all local, so a faithful reconstruction
// sized via measuredOrEstimate (anchored when stable) lets cc.billing show
// the env block as its own static_overhead item — and surfaces that a repo
// with a long status/commit log pays for it on every request.
func environmentBlockText() string {
	cwd := inferCwd()
	if cwd == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Primary working directory: %s\n", cwd)
	fmt.Fprintf(&b, "Platform: %s\n", runtime.GOOS)
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		fmt.Fprintf(&b, "OS Version: %s\n", strings.TrimSpace(string(out)))
	}

	if branch := git(cwd, "branch", "--show-current"); branch != "" {
		fmt.Fprintf(&b, "Is a git repository: true\n")
		fmt.Fprintf(&b, "Current branch: %s\n", branch)
		if status := git(cwd, "status", "--porcelain"); status != "" {
			fmt.Fprintf(&b, "Status:\n%s\n", status)
		} else {
			b.WriteString("Status:\n(clean)\n")
		}
		if log := git(cwd, "log", "--oneline", "-5"); log != "" {
			fmt.Fprintf(&b, "Recent commits:\n%s\n", log)
		}
	}
	return b.String()
}
