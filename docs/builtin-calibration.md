# Calibrating the bundled Claude Code overhead (`cc_builtin`)

The bundled system prompt + built-in tool schemas never appear in the
transcript, so `cc.billing`'s `static_overhead` lane relies on the
per-version table in `src/cc_builtin.go` (`ccBuiltinByVersion`). This doc is
the procedure for refreshing it on each Claude Code release — and, since the
`Components` field landed, for producing the itemized breakdown the
dashboard shows.

## Per-release procedure

1. **Capture one real request** on the new CC version, in a project with NO
   MCP servers connected and auto-memory disabled (so the request is almost
   pure bundled content). Two known-good capture paths:
   - [cost-xray](https://github.com/tigerless-labs/cost-xray): transparent
     local mitmproxy hop; the captured request body contains the full
     `system` block and `tools` array.
   - Any HTTPS-intercepting proxy with the CC CLI's proxy env vars.
2. **Split the `system` block into its named sections** (identity/harness
   rules, security policy, memory instructions, environment template,
   session guidance, context management). The section headings are stable
   markdown headers.
3. **Measure each section** with the free `count_tokens` endpoint (the
   plugin's own `countTokensFor` works, or `curl` — auth with
   `ANTHROPIC_API_KEY` or the CC OAuth token).
4. **Measure the always-on `tools` array** the same way (count with and
   without `tools`, diff). Per-tool figures: add tools one at a time.
5. Add the row:

```go
"2.1.180": {
    SystemPromptTokens:        4900,  // Σ prompt sections
    SystemToolsTokens:         1900,  // tools array
    SystemToolsDeferredTokens: 11300, // /context's deferred row
    Components: map[string]int{
        "identity_and_rules":   2100,
        "security_policy":      300,
        "memory_instructions":  800,
        "environment_template": 200,  // static template only — the dynamic
                                      // part (cwd, git status) is carved out
                                      // at runtime as the `environment` item
        "session_guidance":     600,
        "context_management":   400,
        "builtin_tool_schemas": 1900,
    },
},
```

Invariant: `Σ Components == SystemPromptTokens + SystemToolsTokens`.

## Validation

Run a fresh session on the new version and check the trace's
`cc.billing.lanes.static_overhead.total` against `/context`'s
"System prompt" + "System tools" rows (expect agreement within ~15%; the
always-on tool set varies slightly per session config). The `unattributed`
lane absorbs the difference either way — if it jumps after a CC release,
this table is stale.

## What NOT to do

Do not derive these numbers as a usage residual ("call-1 usage minus known
pieces") — that residual absorbs all unobserved request content (system
reminders, deferred-name listings) and estimation drift; it inflated
static_overhead ~3.6x when tried (see git history).
