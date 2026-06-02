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

Tracing can be enabled at two levels:

- **Project** (default): `.claude/.opik-tracing-enabled` in the current project directory.
- **Global** (`--global` flag): `~/.claude/.opik-tracing-enabled`, which enables tracing for every project.

The project-level file takes precedence over the global one, so a single project can override the global setting (including turning tracing **off** while it is enabled globally).

## File Semantics

For both the project and global files:

- File contains `off` or `disabled` → tracing disabled (used to opt a project out of a global enable)
- File contains `debug` → tracing + debug logging
- File exists with any other content (or empty) → tracing enabled
- File doesn't exist → no opinion at this level (fall through to the next level)

Resolution order (first match wins): project `off` → project enabled → global enabled → disabled.

## Actions

Determine the target file from the flags:
- If `--global` (or `--user`) is present, target is `~/.claude/.opik-tracing-enabled`.
- Otherwise, target is `.claude/.opik-tracing-enabled` in the current directory.

**If the request contains "start":**
- Create the parent `.claude/` directory if needed with `mkdir -p`.
- If `--debug` is present, write `debug` to the target file.
- Otherwise, write an empty file / `touch` the target file (content doesn't matter).
- Confirm scope: "Opik session tracing enabled \[globally for all projects | for this project]. Takes effect immediately for new conversation turns."

**If the request contains "stop":**
- If `--global` is present: delete `~/.claude/.opik-tracing-enabled`. Confirm: "Opik session tracing disabled globally."
- Otherwise (project stop):
  - Check whether the global file `~/.claude/.opik-tracing-enabled` exists and is not itself `off`/`disabled`.
  - If global tracing is active, write `off` to `.claude/.opik-tracing-enabled` (an explicit opt-out that overrides the global enable), and confirm: "Opik session tracing disabled for this project (overriding the global setting)."
  - If global tracing is not active, delete `.claude/.opik-tracing-enabled`, and confirm: "Opik session tracing disabled for this project."

**If the request is "status":**
1. Read the project file `.claude/.opik-tracing-enabled` (if any) and the global file `~/.claude/.opik-tracing-enabled` (if any), interpreting each via the File Semantics above.
2. Report each level: "Project: \[enabled | enabled+debug | opt-out (off) | not set]" and "Global: \[enabled | enabled+debug | not set]".
3. Report the resolved effective state using the resolution order: "Effective: \[enabled | enabled+debug | disabled]".

## Examples

```
/opik:trace-claude-code start                 # Enable session tracing for this project
/opik:trace-claude-code start --debug         # Enable tracing + debug logging for this project
/opik:trace-claude-code stop                  # Disable tracing for this project
/opik:trace-claude-code status                # Check project + global + effective state

/opik:trace-claude-code start --global        # Enable tracing for ALL projects
/opik:trace-claude-code start --global --debug # Enable tracing + debug for ALL projects
/opik:trace-claude-code stop --global         # Disable global tracing
```

## What This Does

When enabled, all your Claude Code interactions are automatically logged to Opik:
- Each conversation turn becomes a trace
- Tool calls, thinking, and responses become spans
- Subagent invocations are nested under their parent

View your traces where they are logged with `opik configure`. 

## Notes

- Changes take effect immediately for new conversation turns (no restart needed)
- Debug logs are written to `$TMPDIR/opik-debug.log`
- Requires Opik configuration (`opik configure` or env vars)
