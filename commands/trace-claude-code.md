---
description: Control Opik tracing for your Claude Code sessions
argument-hint: [start|stop|status] [--debug]
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

Tracing is configured per-project via `.claude/.opik-tracing-enabled` in the project directory.

## File Semantics

- File exists → tracing enabled
- File contains `debug` → tracing + debug logging
- File doesn't exist → tracing disabled

## Actions

**If the request contains "start":**
- Create `.claude/` directory if needed with `mkdir -p`
- If `--debug` is present, write `debug` to `.claude/.opik-tracing-enabled`
- Otherwise, touch/create the file (content doesn't matter)
- Confirm: "Opik session tracing enabled for this project. Takes effect immediately for new conversation turns."

**If the request contains "stop":**
- Delete `.claude/.opik-tracing-enabled`
- Confirm: "Opik session tracing disabled for this project."

**If the request is "status":**
1. Check if `.claude/.opik-tracing-enabled` exists in current directory
2. Report state: "Tracing: [enabled/enabled+debug/disabled]"

## Examples

```
/opik:trace-claude-code start          # Enable session tracing for this project
/opik:trace-claude-code start --debug  # Enable tracing + debug logging
/opik:trace-claude-code stop           # Disable tracing for this project
/opik:trace-claude-code status         # Check current state
```

## What This Does

When enabled, all your Claude Code interactions are automatically logged to Opik:
- Each conversation turn becomes a trace
- Tool calls, thinking, and responses become spans
- Subagent invocations are nested under their parent

View your traces at: https://www.comet.com/opik

## Notes

- Changes take effect immediately for new conversation turns (no restart needed)
- Debug logs are written to `$TMPDIR/opik-debug.log`
- Requires Opik configuration (`opik configure` or env vars)
