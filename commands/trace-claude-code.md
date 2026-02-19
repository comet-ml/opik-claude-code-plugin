---
description: Control Opik tracing for your Claude Code sessions
argument-hint: [start|stop|status] [--global] [--debug]
allowed-tools:
  - Bash
  - Read
  - Write
model: haiku
---

# Opik Claude Code Session Tracing

This command enables/disables automatic tracing of your Claude Code sessions to Opik.

Based on the user's request: **$ARGUMENTS**

## Scope

- **Project-level** (default): `.claude/.opik-tracing-enabled` in the current working directory
- **User-level** (with `--global` flag): `~/.claude/.opik-tracing-enabled`

Project-level settings take precedence over user-level settings.

## File Semantics

- File exists → tracing enabled
- File contains `debug` → tracing + debug logging
- File doesn't exist → tracing disabled

## Actions

**If the request contains "start":**
- Determine scope: global (`~/.claude/`) or project (`.claude/`)
- Create the directory if needed with `mkdir -p`
- If `--debug` is present, write `debug` to the file
- Otherwise, touch/create the file (content doesn't matter)
- Confirm: "Opik session tracing enabled. Restart Claude Code for changes to take effect."

**If the request contains "stop":**
- Determine scope: global (`~/.claude/`) or project (`.claude/`)
- Delete the `.opik-tracing-enabled` file
- Confirm: "Opik session tracing disabled."

**If the request is "status":**
1. Check if `.claude/.opik-tracing-enabled` exists in current directory (project-level)
2. Check if `~/.claude/.opik-tracing-enabled` exists (user-level)
3. Report both settings and the effective state:
   - "Project: [enabled/enabled+debug/disabled/not set]"
   - "Global: [enabled/enabled+debug/disabled/not set]"
   - "Effective: [enabled/disabled] (project takes precedence if set)"

## Examples

```
/opik session start              # Enable session tracing for this project
/opik session start --debug      # Enable tracing + debug logging
/opik session start --global     # Enable tracing globally
/opik session stop               # Disable tracing for this project
/opik session stop --global      # Disable tracing globally
/opik session status             # Check current state
```

## What This Does

When enabled, all your Claude Code interactions are automatically logged to Opik:
- Each conversation turn becomes a trace
- Tool calls, thinking, and responses become spans
- Subagent invocations are nested under their parent

View your traces at: https://www.comet.com/opik

## Notes

- Changes affect NEW sessions only - restart Claude Code for changes to take effect
- Debug logs are written to `$TMPDIR/opik-debug.log`
- Requires Opik configuration (`opik configure` or env vars)
