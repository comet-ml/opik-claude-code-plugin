---
name: opik
description: Control Opik tracing for Claude Code session observability
argument-hint: [start tracing|stop tracing|status] [--global] [--debug]
allowed-tools:
  - Bash
  - Read
  - Write
model: haiku
---

# Opik Tracing Control

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
- If `--debug` is present, write `debug` to the file
- Otherwise, write empty or touch the file
- Confirm the action

**If the request contains "stop":**
- Determine scope: global (`~/.claude/`) or project (`.claude/`)
- Delete the `.opik-tracing-enabled` file
- Confirm: "Opik tracing disabled."

**If the request is "status":**
1. Check if `.claude/.opik-tracing-enabled` exists in current directory (project-level)
2. Check if `~/.claude/.opik-tracing-enabled` exists (user-level)
3. Report both settings and the effective state:
   - "Project: [enabled/enabled+debug/disabled]"
   - "Global: [enabled/enabled+debug/disabled]"
   - "Effective: [enabled/disabled] (project takes precedence)"

## Examples

```
/opik start tracing              # Enable tracing for this project
/opik start tracing --debug      # Enable tracing + debug logging
/opik start tracing --global     # Enable tracing globally
/opik stop tracing               # Disable tracing for this project
/opik stop tracing --global      # Disable tracing globally
/opik status                     # Check current state
```

## Notes

- Changes affect NEW sessions only - restart Claude Code for changes to take effect
- Debug logs are written to `$TMPDIR/opik-debug.log`
