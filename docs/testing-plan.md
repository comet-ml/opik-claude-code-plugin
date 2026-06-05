# Testing plan — token-attribution engine

Verifies the engine emits the `cc.*` schema correctly, attribution math
is consistent, and the calibrated heuristic stays within bounds. Sections
are ordered from cheap (no Claude Code session needed) to expensive (full
end-to-end + edge cases).

Each test has:
- **What** — the check, in plain English
- **How** — exact commands or steps
- **Pass criteria** — what "green" looks like
- **If it fails** — first thing to look at

Replace `<sid>`, `<trace_id>`, `<workspace>`, `<api_key>` with real values
when running. Some checks need bash + python3 + a fresh Claude Code
session inside an instrumented project.

---

## A. Pre-flight — the engine is installed and firing

### A1. Binary is on disk in all 3 plugin paths and matches the latest build

**What:** Claude Code's hook script runs `~/.claude/plugins/marketplaces/opik/bin/opik-logger-darwin-arm64`, but there are also `cache/opik/opik/<ver>/bin/` and `~/.ollie/plugins/opik-claude-code-plugin/bin/` copies. All three must match the dev build.

**How:**
```bash
SRC=~/code/opik-claude-plugin/bin/opik-logger-darwin-arm64
md5 "$SRC"
md5 ~/.claude/plugins/marketplaces/opik/bin/opik-logger-darwin-arm64
md5 ~/.claude/plugins/cache/opik/opik/0.1.0/bin/opik-logger-darwin-arm64
md5 ~/.ollie/plugins/opik-claude-code-plugin/bin/opik-logger-darwin-arm64
```

**Pass:** All four md5s identical.

**If it fails:** Re-run `make build-local && for D in <each>; do cp <bin> $D/; done`.

### A2. Tracing is enabled for the test project

**What:** `~/.claude/.opik-tracing-enabled` or `<project>/.claude/.opik-tracing-enabled` exists. Without it, hooks no-op.

**How:**
```bash
ls -la <project>/.claude/.opik-tracing-enabled ~/.claude/.opik-tracing-enabled 2>/dev/null
```

**Pass:** At least one path exists; content is `debug` for verbose logging or unset for normal mode.

### A3. Debug log is being written

**What:** Start a new Claude Code session in the test project. Make one tool call (e.g. `ls`). Confirm the engine logs.

**How:**
```bash
tail -f $(python3 -c 'import os,tempfile; print(os.path.join(tempfile.gettempdir(),"opik-debug.log"))')
```

**Pass:** You see `=== PostToolUse ===` lines and `flush: N spans` within seconds of each tool call.

**If it fails:** Hook may not be firing — check `~/.claude/plugins/marketplaces/opik/hooks/hooks.json` is registered and CLAUDE_PLUGIN_ROOT is set.

### A4. A trace appears in Opik for the test session

**How:**
```bash
SID=$(ls -t ~/.claude/projects/<workspace>/*.jsonl | head -1 | xargs basename | sed s/.jsonl//)
curl -s -H "Authorization: <api_key>" -H "Comet-Workspace: <workspace>" \
  "https://www.comet.com/opik/api/v1/private/traces?project_name=claude-code&size=1&filters=%5B%7B%22field%22%3A%22thread_id%22%2C%22operator%22%3A%22%3D%22%2C%22value%22%3A%22${SID}%22%2C%22type%22%3A%22string_exact%22%7D%5D" \
  | python3 -m json.tool | head -20
```

**Pass:** A trace exists with `thread_id == SID` and `span_count > 0`.

---

## B. Schema shape — every domain is emitted correctly

### B1. All 9 domains + `cc.llm_call` land on a per-message span

**What:** Fetch any `Bash` span from the latest trace and assert all session-scoped domains are present. Turn-scoped domains are correctly nil when no such content exists in this turn.

| Always present (session-scoped) | Conditionally present (turn-scoped — nil when no data) |
| ------------------------------- | ------------------------------------------------------ |
| `cc.skills`                     | `cc.thinking`                                          |
| `cc.tools`                      | `cc.tool_results`                                      |
| `cc.memory`                     | `cc.user_prompts`                                      |
| `cc.llm_call` (per-message)     | `cc.file_attachments`                                  |
|                                 | `cc.prior_assistant`                                   |
|                                 | `cc.assistant_text`                                    |

**How:**
```bash
python3 <<EOF
import urllib.request, json
TID="<trace_id>"
H = {"Authorization": "<api_key>", "Comet-Workspace": "<workspace>"}
u = f"https://www.comet.com/opik/api/v1/private/spans?project_name=claude-code&trace_id={TID}&size=200"
d = json.loads(urllib.request.urlopen(urllib.request.Request(u, headers=H), timeout=20).read())
bashes = sorted([s for s in d["content"] if s.get("name")=="Bash"], key=lambda s: s.get("start_time",""), reverse=True)
cc = bashes[0]["metadata"]["cc"]
required = {"skills","tools","memory","thinking","tool_results","prior_assistant","assistant_text","llm_call"}
optional = {"user_prompts","file_attachments"}
print("required present:", required - set(cc.keys()) == set())
print("legacy gone:", "context" not in cc and "attribution" not in cc)
print("domains:", sorted(cc.keys()))
EOF
```

**Pass:** `required present: True`, `legacy gone: True`.

**If it fails:** Engine is still emitting the pre-refactor shape. Check `applyDomainSnapshots` is being called and the binary has the new symbols (`strings <bin> | grep cc.skills`).

### B2. The trace itself carries the same shape

**How:**
```bash
curl -s -H "Authorization: <api_key>" -H "Comet-Workspace: <workspace>" \
  "https://www.comet.com/opik/api/v1/private/traces/<trace_id>" | python3 -c '
import sys, json
d = json.load(sys.stdin)
cc = d.get("metadata", {}).get("cc", {})
print("trace cc domains:", sorted(cc.keys()))
'
```

**Pass:** Trace metadata's `cc.*` keys are a superset of the span's (trace also carries the older identity/git fields).

### B3. No legacy `Skill Loaded:` spans on a fresh trace

**How:**
```bash
python3 -c '
import urllib.request, json
... # fetch all spans
legacy = sum(1 for s in spans if s["name"].startswith("Skill Loaded:") or s["name"].startswith("Skill Invoked:"))
print("legacy:", legacy)
'
```

**Pass:** `legacy: 0`. (Historic traces from before the rewrite will still have these — known issue, no API for span deletion.)

---

## C. Per-domain correctness

### C1. `cc.skills.summary.available_count` matches the menu

**What:** The number of skills in `cc.skills.available` should equal `skill_listing.skillCount` from the transcript.

**How:**
```bash
# Read skillCount from the transcript
python3 -c '
import json
sid = "<sid>"
with open(f"/Users/collinc/.claude/projects/<workspace>/{sid}.jsonl") as f:
    for line in f:
        e = json.loads(line)
        if e.get("type")=="attachment" and (e.get("attachment") or {}).get("type")=="skill_listing":
            print("transcript skillCount:", e["attachment"]["skillCount"])
            break
'
# Compare against Opik
python3 -c '... cc.skills.summary.available_count ...'
```

**Pass:** Numbers match.

### C2. Each on-disk skill has a real SHA, and SHAs differ across skills

**How:**
```bash
python3 -c '
import urllib.request, json
... fetch any span ...
skills = cc["skills"]["available"]
on_disk = [s for s in skills if s.get("source") == "listing"]
distinct = {s.get("sha256") for s in on_disk if s.get("sha256")}
print(f"on-disk: {len(on_disk)}, distinct SHAs: {len(distinct)}")
'
```

**Pass:** `distinct == len(on_disk)` (or very close — there may be 1–2 exact-duplicate skills).

**If it fails:** Per-skill resolver is broken — falling back to listing-blob hash for all entries.

### C3. `cc.skills.loaded` reflects actual Skill tool invocations

**What:** Trigger a Skill tool call mid-session, then verify it appears in `loaded[]`.

**How:**
1. From the active Claude Code session, ask "load the find-skills skill".
2. After 8s flush gate, query the latest trace.

```bash
python3 -c '... newest span ... cc.skills.loaded ...'
```

**Pass:** `loaded[]` includes `{"name": "find-skills", "sha256": "...", "body_tokens": N, "source": "..."}` where N is non-zero (or zero only for bundled with no body yet).

### C4. Failed Skill calls are NOT in `loaded[]`

**What:** Invoking a non-existent skill returns an error, no body delivered. It should not appear in `loaded[]`.

**How:** Attempt `Skill(skill="this-does-not-exist")`, then check `cc.skills.loaded` doesn't include `this-does-not-exist`.

**Pass:** Failed skill absent.

### C5. The `Skill` tool_use span carries `cc.skills.load`

**How:** Look for span named `Skill`:
```bash
python3 -c '
... find spans with name=="Skill" ...
load = s["metadata"]["cc"]["skills"]["load"]
print(load)  # should have name, sha256, body_tokens, source, tool_use_id (+ path if on-disk)
'
```

**Pass:** `cc.skills.load` exists with the loaded skill's identity, matching one row in `cc.skills.loaded[]`.

### C6. `cc.tools.summary.by_server` lists all registered MCP servers

**What:** Query `~/.claude.json` or similar for enabled MCPs, compare against `by_server[]`.

**Pass:** Same set of server names.

### C7. Built-in + MCP tools sum equals `cc.tools.summary.available_count`

```bash
python3 -c '
... fetch ...
tools = cc["tools"]
assert tools["summary"]["available_count"] == tools["summary"]["by_source"]["builtin"]["available_count"] + tools["summary"]["by_source"]["mcp"]["available_count"]
print("ok")
'
```

**Pass:** Equation holds.

### C8. `cc.memory.files[]` contains the CLAUDE.md / MEMORY.md files that exist on disk

```bash
ls -la ~/.claude/CLAUDE.md <project>/CLAUDE.md <project>/.claude/CLAUDE.md ~/.claude/projects/<workspace>/memory/*.md 2>/dev/null
python3 -c '... cc.memory.files ...'
```

**Pass:** Every existing file appears in `cc.memory.files[]`, none missing.

### C9. `cc.user_prompts.summary.bucket` matches token range

**Pass:** `total_tokens < 500 → bucket=small`, `500–2000 → medium`, `2k–8k → large`, `>8k → xlarge`. Spot-check on a turn whose prompt size you know.

### C10. `cc.tool_results.by_tool` sums to `summary.total_tokens`

```bash
python3 -c '
... fetch ...
tr = cc["tool_results"]
total = sum(x["tokens"] for x in tr["by_tool"])
assert total == tr["summary"]["total_tokens"], f"{total} != {tr[\"summary\"][\"total_tokens\"]}"
print("ok")
'
```

---

## D. `cc.llm_call` — the critical correctness check

### D1. Every per-message span has `cc.llm_call.message_id`

**Pass:** 100% coverage. Spans without (e.g. context-init only) are also OK if no message_id exists.

### D2. Spans from the same LLM call share a `message_id`

**How:**
```bash
python3 -c '
... fetch all spans ...
from collections import defaultdict
by_mid = defaultdict(list)
for s in spans:
    mid = s.get("metadata",{}).get("cc",{}).get("llm_call",{}).get("message_id")
    if mid: by_mid[mid].append(s)
multi = {m:v for m,v in by_mid.items() if len(v) > 1}
print(f"distinct LLM calls: {len(by_mid)}, multi-block: {len(multi)}")
for mid, sps in list(multi.items())[:3]:
    print(mid[:24], "→", [s["name"] for s in sorted(sps, key=lambda x: x["metadata"]["cc"]["llm_call"]["block_index"])])
'
```

**Pass:** Multi-block calls show `[Thinking, Text, Edit]` or similar — same `message_id`, increasing `block_index`.

### D3. `Σ attributed_output_tokens == anchor.usage.completion_tokens` for every LLM call

**This is the headline guarantee** of the attribution rewrite — the per-block split must sum back to the true API output.

```bash
python3 -c '
... group by message_id ...
exact = off = 0
for mid, sps in by_mid.items():
    sps.sort(key=lambda s: s["metadata"]["cc"]["llm_call"]["block_index"])
    anchor = (sps[0].get("usage") or {}).get("completion_tokens", 0)
    attr_sum = sum(s["metadata"]["cc"]["llm_call"].get("attributed_output_tokens",0) for s in sps)
    if not anchor: continue
    if anchor == attr_sum: exact += 1
    else: off += 1; print(f"  Δ {mid[:24]}: anchor={anchor} Σattr={attr_sum}")
print(f"exact: {exact}, off: {off}")
'
```

**Pass:** `off == 0`.

**If it fails:** `distributeAttribution` has a bug — most likely the thinking-leftover case or the rounding remainder.

### D4. Per-category attribution `GROUP BY block_kind` produces real proportions

**The whole point of the attribution work.**

```bash
python3 -c '
... aggregate ...
from collections import defaultdict
by_kind = defaultdict(int)
for s in spans:
    llm = s.get("metadata",{}).get("cc",{}).get("llm_call",{})
    by_kind[llm.get("block_kind","?")] += llm.get("attributed_output_tokens",0)
for k, v in sorted(by_kind.items(), key=lambda kv: -kv[1]):
    print(f"  {k:<12} {v:>8} tokens")
'
```

**Pass:** Thinking, text, tool_use are all non-zero; sum ≈ trace's `total_estimated_cost` worth of completion tokens; **thinking is not 100% of the total** (it used to be, pre-rewrite).

---

## E. Idempotence — re-flushes don't duplicate

### E1. Span count stays bounded across multiple flushes in one turn

**How:** Make 10 tool calls in one turn (don't end the turn). Track span_count over time.

```bash
for i in $(seq 1 10); do
  sleep 6
  curl -s -H "Authorization: <api_key>" -H "Comet-Workspace: <workspace>" \
    "https://www.comet.com/opik/api/v1/private/traces/<trace_id>" \
    | python3 -c 'import sys,json; print("span_count:", json.load(sys.stdin).get("span_count"))'
done
```

**Pass:** span_count grows monotonically by ~1–3 per flush (one per new tool call), NOT by hundreds (which would mean Skill Loaded duplicates are back).

**If it fails:** Either `applyDomainSnapshots` is creating new spans per flush, or `toV7(message.UUID)` isn't being used as the span ID. Check `processTranscriptEntries` keeps `span.ID = toV7(p.UUID)`.

### E2. Span IDs deterministic — re-running the binary on the same transcript produces the same span IDs

**How:** Run the binary manually against a captured transcript twice (different processes), confirm identical span IDs.

```bash
SID=<sid>
PAYLOAD=$(echo "{\"hook_event_name\":\"PostToolUse\",\"session_id\":\"$SID\",\"transcript_path\":\"$HOME/.claude/projects/<workspace>/$SID.jsonl\"}")
# Run twice — log both runs' span IDs from the API POST body
# (would need to instrument or sniff)
```

**Pass:** Same set of UUIDs across runs.

---

## F. Calibration accuracy

### F1. Re-run the calibrator and confirm per-type ratios are within ±0.2 of the documented numbers

**How:**
```bash
cd ~/code/opik-claude-plugin/tools/calibration
source <venv>/bin/activate
ANTHROPIC_API_KEY=<key> python calibrate_tokenizer.py
python evaluate_heuristic.py
```

**Pass:** `median chars/tok` per type within ±0.2 of:
- deferred_tools_payload: 2.5
- tool_use_input: 2.8
- tool_result: 3.0
- skill_body: 3.5
- assistant_text: 3.9
- user_prompt: 4.3

If your transcripts include different content distributions, ratios may drift slightly — that's expected. Document the new ratios and update `charsPerToken` if drift exceeds ±0.3.

### F2. Spot-check one in-trace estimate against count_tokens

Pick any `Skill Loaded:` span body or `Bash` tool input from the live trace, run it through count_tokens, compare to the metadata's `body_tokens` / `result_tokens`. Should be within ±15%.

---

## G. Edge cases

### G1. `/compact` boundary

**How:** During a long session, run `/compact`. A new trace is created.

**Pass on the post-compact trace:** all 9 domains still populate, `cc.skills.loaded[]` reflects whatever was carried over.

### G2. Subagent (Task tool)

**How:** Trigger a `Task` subagent (any Agent invocation).

**Pass:** Child spans on the parent trace have `cc.skills.available`, `cc.tools.*`, etc., set from the subagent's own transcript. Parent span output is patched on Stop.

### G3. Failed tool call

**How:** Force a Bash error (`Bash(command="false")`).

**Pass:** The `Bash` span has `error` set, AND `cc.tool_results` for that turn still counts it (failed results still consume tokens).

### G4. Empty turn (no tool calls, no skill loads)

**How:** A turn that's only text — no tool calls.

**Pass:** Trace exists, has thinking + text spans, `cc.tool_results.summary` is nil (correctly omitted), other domains populated.

### G5. Long transcript performance

**How:** Time `flush()` on a 7000-line transcript.

```bash
ls -la /var/folders/.../T/opik-debug.log    # find the temp log
grep "flush: " <log> | awk -F: '{print $1}'  # eyeball flush times
```

Or instrument with a `time.Now()` around the flush call in main.go.

**Pass:** Each flush completes in under 200ms. If it's over 1s, profile — likely the double transcript read in `flush()` + `postTraceMetrics`.

### G6. `user_prompts.summary` only populates when there's actual user text in the turn

**Pass:** If the user message is a tool_result only (mid-turn), `cc.user_prompts` is nil. If it's a text prompt, it's populated.

---

## H. Negative tests — things that should NOT be there

### H1. No `cc.context.*` key (deprecated)

```bash
python3 -c '... assert "context" not in cc'
```

### H2. No `cc.attribution.*` key at the top level (moved under `cc.skills.load`)

```bash
python3 -c '... assert "attribution" not in cc'
```

### H3. No timestamps in any `cc.*` domain

```bash
python3 -c '
import json
def find_ts(o, path=""):
    if isinstance(o, dict):
        for k, v in o.items():
            if "time" in k.lower() or "_at" in k.lower():
                print(f"FOUND ts-like: {path}.{k} = {v}")
            find_ts(v, f"{path}.{k}")
    elif isinstance(o, list):
        for i, v in enumerate(o):
            find_ts(v, f"{path}[{i}]")
find_ts(cc, "cc")
'
```

**Pass:** No output (no timestamp-shaped fields under `cc.*`).

### H4. Span IDs are stable across re-flushes within a turn

Already covered by E1, listed here for completeness.

---

## I. The end-to-end smoke

A scripted scenario that exercises most domains in one session:

1. Start a fresh Claude Code session inside a fresh-checkout of the target repo.
2. Send prompt: *"Read the README.md, then write a one-line summary to /tmp/summary.txt. Also use the find-skills skill if it's available, and list one of your MCP server's tools."*
3. After Stop, fetch the trace and verify:
   - `cc.skills.loaded[]` includes `find-skills` (sha + body_tokens > 0)
   - `cc.tools.summary.by_source.mcp.available_count > 0`
   - `cc.tool_results.by_tool` has entries for `Read`, `Write`, and at least one `mcp__*` tool
   - `cc.assistant_text.summary.total_tokens > 0`
   - `cc.user_prompts.summary.total_tokens > 0`
   - `cc.llm_call.message_id` is on every per-message span
   - For every LLM call: `Σ attributed_output_tokens == anchor.completion_tokens`

If all of those check, the engine passes.

---

## Quick sanity script

Save this to `tools/verify.py` and run after any change:

```python
#!/usr/bin/env python3
"""Verify the latest trace passes the headline guarantees."""
import os, sys, json, urllib.request
from collections import defaultdict

SID = os.environ["SID"]
H = {"Authorization": os.environ["OPIK_KEY"],
     "Comet-Workspace": os.environ.get("OPIK_WORKSPACE", "comet-all")}

def fetch(url):
    return json.loads(urllib.request.urlopen(urllib.request.Request(url, headers=H), timeout=20).read())

# Latest trace
filt = f'%5B%7B%22field%22%3A%22thread_id%22%2C%22operator%22%3A%22%3D%22%2C%22value%22%3A%22{SID}%22%2C%22type%22%3A%22string_exact%22%7D%5D'
latest = fetch(f"https://www.comet.com/opik/api/v1/private/traces?project_name=claude-code&size=1&filters={filt}")["content"][0]
TID = latest["id"]
print(f"trace {TID}  input: {latest.get('input',{}).get('text','')[:60]!r}")

# All spans
spans = []
for p in range(1, 10):
    d = fetch(f"https://www.comet.com/opik/api/v1/private/spans?project_name=claude-code&trace_id={TID}&size=200&page={p}")
    rows = d.get("content", [])
    spans.extend(rows)
    if len(rows) < 200: break

assert spans, "no spans"

# B1: every required domain present on a tool span
sample = next((s for s in spans if s.get("name") == "Bash"), spans[0])
cc = sample["metadata"]["cc"]
required = {"skills","tools","memory","thinking","tool_results","prior_assistant","assistant_text","llm_call"}
missing = required - set(cc.keys())
assert not missing, f"missing domains: {missing}"
print(f"  B1 ✓ all domains present on {sample.get('name')}")

# H1/H2: no legacy keys
assert "context" not in cc, "legacy cc.context still present"
assert "attribution" not in cc, "legacy cc.attribution still present"
print(f"  H1/H2 ✓ no legacy keys")

# D3: attribution sums per LLM call
by_mid = defaultdict(list)
for s in spans:
    llm = s.get("metadata",{}).get("cc",{}).get("llm_call",{})
    if llm.get("message_id"):
        by_mid[llm["message_id"]].append(s)
mismatches = 0
for mid, sps in by_mid.items():
    sps.sort(key=lambda x: x["metadata"]["cc"]["llm_call"]["block_index"])
    anchor = (sps[0].get("usage") or {}).get("completion_tokens", 0)
    if not anchor: continue
    attr = sum(s["metadata"]["cc"]["llm_call"].get("attributed_output_tokens",0) for s in sps)
    if anchor != attr:
        mismatches += 1
        print(f"  Δ {mid[:24]}: anchor={anchor} Σattr={attr}")
assert mismatches == 0, f"{mismatches} LLM calls with attribution mismatch"
print(f"  D3 ✓ all {len(by_mid)} LLM calls' attribution sums to anchor.usage")

# D4: per-category breakdown
by_kind = defaultdict(int)
for s in spans:
    llm = s.get("metadata",{}).get("cc",{}).get("llm_call",{})
    by_kind[llm.get("block_kind","?")] += llm.get("attributed_output_tokens",0)
print(f"  D4 per-category output token attribution:")
for k, v in sorted(by_kind.items(), key=lambda kv: -kv[1]):
    print(f"     {k:<12} {v:>8}")

print(f"\n✓ all headline checks passed for trace {TID}")
```

Run with:
```bash
SID=$(ls -t ~/.claude/projects/<workspace>/*.jsonl | head -1 | xargs basename | sed s/.jsonl//) \
OPIK_KEY=<key> \
python3 verify.py
```

A green output here means the engine is producing the contract the FE/queries depend on.
