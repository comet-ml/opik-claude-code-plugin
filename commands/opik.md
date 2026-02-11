---
name: opik
description: Control Opik tracing for Claude Code session observability
argument-hint: [start tracing|stop tracing|status] [--global]
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

## Actions

**If the request contains "start" (e.g., "start tracing", "start"):**
- If `--global` is present:
  1. Run `mkdir -p ~/.claude`
  2. Write `true` to `~/.claude/.opik-tracing-enabled`
  3. Confirm: "Opik tracing enabled globally. New sessions will be traced (unless overridden by project settings)."
- Otherwise (project-level):
  1. Run `mkdir -p .claude`
  2. Write `true` to `.claude/.opik-tracing-enabled`
  3. Confirm: "Opik tracing enabled for this project. New sessions in this project will be traced."

**If the request contains "stop" (e.g., "stop tracing", "stop"):**
- If `--global` is present:
  1. Run `mkdir -p ~/.claude`
  2. Write `false` to `~/.claude/.opik-tracing-enabled`
  3. Confirm: "Opik tracing disabled globally."
- Otherwise (project-level):
  1. Run `mkdir -p .claude`
  2. Write `false` to `.claude/.opik-tracing-enabled`
  3. Confirm: "Opik tracing disabled for this project."

**If the request is "status":**
1. Check if `.claude/.opik-tracing-enabled` exists in current directory (project-level)
2. Check if `~/.claude/.opik-tracing-enabled` exists (user-level)
3. Report both settings and the effective state:
   - "Project: [enabled/disabled/not set]"
   - "Global: [enabled/disabled/not set]"
   - "Effective: [enabled/disabled] (project takes precedence)"

## Notes

- Changes affect NEW sessions only - the current session's tracing state was determined at startup
- If neither file exists, tracing defaults to disabled
