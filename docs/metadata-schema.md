# `metadata.cc.*` schema design

Stable, future-proof shape for everything the engine writes to Opik. Each
top-level key under `cc.*` is a **single domain** (skills, mcp, tools, memory,
…). No prefix duplication, no mixing of "summary" totals and detail arrays at
the same level.

This doc covers **skills** end to end. MCP and the rest are stubs to be
fleshed out in the same shape.

---

## Where this lands

- **Span metadata** (`span.metadata.cc.*`) — written by `processTranscriptEntries` on every per-message span (Thinking, Text, every tool_use). Carries the **context as it was at the moment that span ran** so the span is self-describing.
- **Trace metadata** (`trace.metadata.cc.*`) — written by `postTraceMetrics` once at `Stop`. Carries the **same shape**, scoped to the whole turn.

Same shape both places — consumers don't need branchy code.

---

## `cc.skills` schema

```jsonc
"cc": {
  "skills": {
    "summary": {
      "total_tokens":     136227,   // menu + loaded; the bill the model is paying for "skills" this turn
      "menu_tokens":      1601,     // cost of the listing itself (paid every turn)
      "loaded_tokens":    134626,   // cost of bodies in conversation history (paid every turn from invocation onward)
      "available_count":  34,       // how many skills are in the menu
      "loaded_count":     8         // how many have actually been pulled in
    },

    // Menu entries — what the model COULD load. No body in context yet, so no
    // body_tokens here. The sha is of the on-disk SKILL.md (identity tag), so
    // cross-session queries can dedupe "is this the same skill version".
    "available": [
      {
        "name":   "find-skills",
        "source": "listing",                            // "listing" | "bundled"
        "sha256": "1e85f6f9686e14ed3b…",                // omitted for bundled
        "path":   "/Users/collinc/.claude/skills/find-skills/SKILL.md"
      },
      {
        "name":   "keybindings-help",
        "source": "bundled"
        // no sha or path — no on-disk file
      }
      // … one entry per skill in the menu
    ],

    // Bodies actually in context for this trace. These are the ones being paid
    // for on every prompt-caching read. Each one was loaded by some Skill
    // tool_use earlier in the session.
    "loaded": [
      {
        "name":          "opik-backend",
        "source":        "listing",
        "sha256":        "b7111954d59f32…",      // of the BODY as it landed in the conversation (so the args/wrapper changes the hash)
        "body_tokens":   487,
        "path":          "/Users/collinc/code/opik/.claude/skills/opik-backend/SKILL.md",
        "tool_use_id":   "toolu_016Cn2ebcdN4KPnkzGAW63pN"
      },
      {
        "name":          "claude-api",
        "source":        "bundled",
        "sha256":        "aa63f62c2ed9bf…",
        "body_tokens":   128306,
        "tool_use_id":   "toolu_01CSGcCP3JzFCPEnm4Xbpt23"
        // no path — bundled skill has no on-disk source we can resolve
      }
      // … one entry per distinct skill loaded so far in the session
    ],

    // Only present on a `Skill` tool_use span. Identifies which load event this
    // particular span represents. Same identity as the matching `loaded[]` row.
    // Kept as a sub-key (not a parallel top-level) so consumers always find
    // skill-related fields under cc.skills.
    "load": {
      "name":         "opik-backend",
      "source":       "listing",
      "sha256":       "b7111954d59f32…",
      "body_tokens":  487,
      "path":         "/Users/collinc/code/opik/.claude/skills/opik-backend/SKILL.md",
      "tool_use_id":  "toolu_016Cn2ebcdN4KPnkzGAW63pN"
    }
  }
}
```

### Field rationale

| Field                    | Why                                                                                              |
| ------------------------ | ------------------------------------------------------------------------------------------------ |
| `summary.*_tokens`       | Three numbers, one per cost lane. Same names as the FE drillKeys so the dashboard maps 1:1.      |
| `summary.*_count`        | Counts kept beside their totals; no scattered "n_skills_loaded" elsewhere.                       |
| `available[].sha256`     | Identity tag for the on-disk SKILL.md so cross-session queries dedupe by version. Cheap to compute. |
| `available[].source`     | Only two values; clean enum, no `is_bundled` boolean flag noise.                                 |
| `loaded[].sha256`        | Hash of the **transcript body** (not the on-disk file) — captures what the model actually saw, args + wrappers included. |
| `loaded[].tool_use_id`   | Stable link from this entry to the Skill span that loaded it. The FE drill-in is one query.     |
| `load.*`                 | Only present on the Skill tool_use span itself. Same identity fields as `loaded[]`, so a query for "all spans that loaded opik-backend" is just `cc.skills.load.name == "opik-backend"`. |

### Why "loaded body sha" differs from "available sha"

A clarification we hit during testing: the body delivered to the model is the on-disk file content **plus** the args echo and Claude Code's wrapper:

```
Base directory for this skill: /Users/.../opik-backend
…<on-disk SKILL.md content>…
ARGUMENTS: testing per-skill SHA on new trace
```

So `available[].sha256` (on-disk) ≠ `loaded[].sha256` (in conversation) for the same skill name. Both are useful and intentionally distinct:

- `available[].sha256` answers "is the source file the same as some other workspace's?"
- `loaded[].sha256` answers "is the body in this conversation the same as the body in some other conversation?" (i.e. were they invoked with the same args)

### What `metadata.cc.context` becomes

Deprecated. Everything that was under `cc.context.*` moves under `cc.skills.*`. Tools (built-in + MCP) live under `cc.tools.*`, memory under `cc.memory.*`, etc. Migration is one PR; no consumer outside the engine reads it yet.

---

## Mapping each `cc.skills.*` to the FE Coding Harness drillKeys

| FE drillKey         | source path on every span                                       |
| ------------------- | --------------------------------------------------------------- |
| `skills_available`  | `metadata.cc.skills.summary.menu_tokens` (lane total), drill-in opens `metadata.cc.skills.available` |
| `skills_loaded`     | `metadata.cc.skills.summary.loaded_tokens` (lane total), drill-in opens `metadata.cc.skills.loaded` |
| (skill load event)  | spans where `metadata.cc.skills.load` exists                    |

---

---

## `cc.tools` schema (built-ins + MCP, unified)

MCP tools are not a separate category — they're just **tools with an
`mcp__<server>__<tool>` name prefix**, declared via `deferred_tools_delta`
attachments instead of the base system prompt. Same `tool_use` →
`tool_result` mechanic, same "in the catalog every turn" cost model. The
unified `cc.tools.*` domain covers built-ins and MCP together; consumers
slice by `source` or `server` when they want MCP-only views.

```jsonc
"cc": {
  "tools": {
    "summary": {
      "total_tokens":     2460,    // schema cost only — call cost lives on individual tool_use spans
      "schema_tokens":    2460,    // cost of all tool definitions in the system prompt this turn (built-in + MCP)
      "available_count":  101,     // 18 built-in + 83 MCP
      "by_source": {
        "builtin": { "available_count": 18, "schema_tokens": 640 },
        "mcp":     { "available_count": 83, "schema_tokens": 1820 }
      },
      "by_server": [                // MCP-only — drill into per-server schema overhead
        { "server": "chrome-devtools",     "tool_count": 28, "schema_tokens": 540 },
        { "server": "claude_ai_Atlassian", "tool_count": 24, "schema_tokens": 620 }
        // …
      ]
    },

    // Full catalog — built-ins and MCP tools side by side.
    "available": [
      { "name": "Bash",  "source": "builtin" },
      { "name": "Read",  "source": "builtin" },
      { "name": "Edit",  "source": "builtin" },
      // …
      {
        "name":   "mcp__chrome-devtools__navigate_page",
        "source": "mcp",
        "server": "chrome-devtools",
        "tool":   "navigate_page"
      },
      {
        "name":   "mcp__claude_ai_Atlassian__search",
        "source": "mcp",
        "server": "claude_ai_Atlassian",
        "tool":   "search"
      }
      // …
    ]

    // NO `calls[]` and NO `summary.call_*` fields.
    // The existing engine already emits one span per tool_use, with
    // `span.name` == the tool name (e.g. "Bash" or "mcp__chrome-devtools__navigate_page"),
    // `span.input` == the args, `span.output` == the result, and `span.error` set on failure.
    // Anything you'd want about "calls in this trace" is on those spans —
    // putting it in metadata too would be pure duplication.
    // FE queries:
    //   - per-server call breakdown:  filter spans where name LIKE 'mcp__<server>__%'
    //   - per-tool call count:         GROUP BY span.name
    //   - call_tokens for a server:    SUM(len(json(span.input)) + len(json(span.output))) over those spans
  }
}
```

### Field rationale

| Field | Why |
| --- | --- |
| `source: "builtin" \| "mcp"` | One enum, consumers query by it. No parallel `cc.mcp.*` domain to keep in sync. |
| `summary.by_source` | Quick "built-in vs MCP" schema-cost split for the FE without paging through `available[]`. |
| `summary.by_server[]` | MCP servers are the unit of operational management ("disable Atlassian"). Per-server overhead answers "which MCP server is worth keeping". |
| `available[].server, .tool` | Pre-split convenience. The FE doesn't have to parse `mcp__<server>__<tool>` itself. |
| no `calls[]` array | Each call is already a span — duplicating it in metadata helps no one. |
| no `summary.call_count` / `call_tokens` | Derivable from spans by name; not worth shadow-copying. |
| no per-span `cc.tools.call` hook key | Same reason — the span IS the call. |

### Per-tool schema_tokens

We can attribute schema cost per-server by measuring each server's portion
of `deferred_tools_delta.addedNames` payload. Per-built-in-tool schema
isn't exposed in the transcript (it lives in Claude Code's system prompt),
so `by_source.builtin.schema_tokens` stays a single bulk number until we
have a way to slice it.

---

## `cc.memory` schema

CLAUDE.md / MEMORY.md / `.agents/` files loaded at session start.

```jsonc
"cc": {
  "memory": {
    "summary": {
      "total_tokens": 1840,
      "file_count":   3
    },
    "files": [
      {
        "path":        "/Users/collinc/.claude/CLAUDE.md",
        "sha256":      "ab12cd34…",
        "body_tokens": 540
      },
      {
        "path":        "/Users/collinc/code/opik/CLAUDE.md",
        "sha256":      "ef56gh78…",
        "body_tokens": 1200
      },
      {
        "path":        "/Users/collinc/.claude/projects/-Users-collinc-code-opik/memory/MEMORY.md",
        "sha256":      "ij90kl12…",
        "body_tokens": 100
      }
    ]
  }
}
```

---

## `cc.thinking` schema

Per-turn aggregate of thinking tokens bucketed by effort level. Level is
inferred from actual tokens per LLM call — the transcript does not expose
the requested `budget_tokens`.

| Level | Thinking tokens per call |
|-------|--------------------------|
| `minimal` | ≤ 500 |
| `light` | 501 – 3 000 |
| `medium` | 3 001 – 10 000 |
| `heavy` | > 10 000 |

```jsonc
"cc": {
  "thinking": {
    "summary": {
      "total_tokens": 9230,
      "call_count":   5
    },
    "by_level": [
      { "level": "minimal", "tokens":  130, "call_count": 2 },
      { "level": "light",   "tokens": 1000, "call_count": 1 },
      { "level": "heavy",   "tokens": 8100, "call_count": 2 }
    ]
  }
}
```

> Per-span: the existing engine already emits one `Thinking` span per
> thinking block with `span.usage` populated. The trace-level rollup above
> is enough; no `cc.thinking.event` needed on individual spans.

---

## `cc.tool_results` schema

Aggregate of bytes flowing back to the model from tool_results this turn.
Per-span detail is already on the matching `tool_use` span (via the
`cc.tools.calls[]` row that joins back by `tool_use_id`); this is the
turn-level lane total.

```jsonc
"cc": {
  "tool_results": {
    "summary": {
      "total_tokens": 18420,
      "count":        31
    },
    "by_tool": [
      { "name": "Bash",  "tokens": 14200, "count": 18 },
      { "name": "Read",  "tokens":  3100, "count":  7 },
      { "name": "Edit",  "tokens":     0, "count":  4 },
      { "name": "Grep",  "tokens":  1120, "count":  2 }
    ]
  }
}
```

---

## `cc.user_prompts` schema

```jsonc
"cc": {
  "user_prompts": {
    "summary": {
      "total_tokens": 142,
      "count":        1,
      "bucket":       "small"    // small | medium | large | xlarge — based on total_tokens
    }
  }
}
```

> Per-turn there's typically one prompt. Buckets: `small <500`, `medium 500–2k`,
> `large 2k–8k`, `xlarge >8k`. No `prompts[]` array — the prompt text is
> already on the trace.input.

---

## `cc.file_attachments` schema

@-mentioned files and system-injected attachments (excluding skill bodies — those go under `cc.skills.loaded`), grouped by file extension.

```jsonc
"cc": {
  "file_attachments": {
    "summary": {
      "total_tokens": 12400,
      "file_count":   4
    },
    "by_type": [
      { "ext": ".tsx",  "tokens": 8200, "file_count": 1 },
      { "ext": ".md",   "tokens": 3100, "file_count": 2 },
      { "ext": "other", "tokens": 1100, "file_count": 1 }  // no extension
    ]
  }
}
```

---

## `cc.prior_assistant` schema

Cumulative cost of all prior assistant output in the session — what gets
replayed every turn. Per-turn signal of "context grown so far".

```jsonc
"cc": {
  "prior_assistant": {
    "summary": {
      "total_tokens": 87000,   // sum of all prior assistant output_tokens up to but not including this turn
      "turn_count":   23
    }
  }
}
```

> No detail array — the prior turns are queryable through Opik (thread_id).

---

## `cc.assistant_text` schema

```jsonc
"cc": {
  "assistant_text": {
    "summary": {
      "total_tokens": 4200,
      "block_count":  6
    }
  }
}
```

> Per-bucket category breakdown (code vs explanation vs summary)
> intentionally deferred — requires content classification we don't have
> yet. Aggregate now; refine later.

---

## `cc.llm_call` schema (per-span origin tag)

Per-span only (never on the trace). Tells you which LLM call a span came
from. Critical for token attribution: **Claude Code splits a single LLM
call into multiple transcript entries** (one per content block — thinking,
text, tool_use), all sharing the same `message.id` and `message.usage`.
The engine's `DeduplicateUsage` step attaches the total usage to only the
**first** block to avoid 3×-counting, which means naive queries like
`GROUP BY span.name` over `usage` make it look like all tokens came from
Thinking. Group by `cc.llm_call.message_id` to recover the true per-call
totals.

```jsonc
"cc": {
  "llm_call": {
    "message_id":               "msg_01abc…",  // Anthropic message id from `message.id`
    "block_index":              0,             // 0-based position of this block within its message_id group
    "block_kind":               "thinking",    // "thinking" | "text" | "tool_use"
    "measured_output_tokens":   0,             // chars/4 estimate of THIS block's content (0 for thinking — text redacted)
    "attributed_output_tokens": 9230           // this block's share of `message.usage.output_tokens`
  }
}
```

### Field rationale

| Field | Why |
| --- | --- |
| `message_id` | The Anthropic id is the only stable identifier for "this LLM call". Same id across all blocks. |
| `block_index` | Order within the message. The block at index 0 is the dedup anchor — it carries `span.usage`; others have nil. |
| `block_kind` | Matches the content type. Convenience for queries that don't want to also read `span.name`. |
| `measured_output_tokens` | Directly observable for text + tool_use; zero for thinking. |
| `attributed_output_tokens` | This block's share of the LLM call's actual `output_tokens`. For text/tool_use: equals `measured_output_tokens`. For thinking: leftover (`total - sum(measured)`, split across thinking blocks). |

### Recipe for correct per-category token attribution

```
SELECT cc.llm_call.block_kind, SUM(cc.llm_call.attributed_output_tokens)
GROUP BY cc.llm_call.block_kind
```

That gives you real per-category output-token cost: text was X, tool_use
was Y, thinking was Z, summing to the trace's actual output. Naive
`GROUP BY span.name` over `span.usage.completion_tokens` still works for
the LLM-call total (only the anchor carries it; sum is one row per LLM
call). Use `attributed_output_tokens` for the per-category split.

---

## Already-emitted domains (no design change)

- **`cc.identity`** — `org_uuid`, `org_name`, `user_uuid`, `user_email`, `user_display_name`. Predates this design; leave as is.
- **`cc.repository`, `cc.branch`, `cc.head_sha_start`, `cc.head_sha_end`, `cc.commits_in_trace`, `cc.lines_*`, `cc.files_authored`, `cc.uncommitted_*`** — git metrics from `metrics.go`. These should eventually move under `cc.git.*` for consistency, but doing that is a separate migration and breaks nothing right now.

---

## Domain conventions (apply to every new `cc.<domain>`)

1. **`summary` block first**, with `*_tokens` numbers and `*_count` counts. No scattered scalars at the domain root.
2. **Detail arrays use the same key names across domains** where they mean the same thing: `body_tokens`, `tool_use_id`, `sha256`, `path`, `source`.
3. **A per-event hook key** (singular form, e.g. `cc.skills.load`) is only present on the span that represents that event, and only when the existing engine doesn't already name the span after that event. Same identity shape as the matching row in the domain's array.
4. **No timestamps anywhere**. The transcript and Opik already record them; duplicating them inside `cc.*` is noise.
5. **No booleans where an enum is clearer**. `source: "listing" | "bundled"` beats `is_bundled: true`.
6. **Bundled / unresolvable cases** still get a row — `sha256` omitted, `body_tokens: 0` is allowed when we genuinely have no body to measure.
