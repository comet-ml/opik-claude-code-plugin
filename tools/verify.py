#!/usr/bin/env python3
"""Verify the latest trace passes the headline guarantees.

Usage:
  SID=<session-id> OPIK_KEY=<api-key> [OPIK_WORKSPACE=<ws>] python3 verify.py

Where <session-id> is the basename of the .jsonl in
~/.claude/projects/<workspace>/ — e.g. `f3d5f52e-9b90-4927-bcf2-a3d75e414006`.
"""
from __future__ import annotations
import json
import os
import sys
import urllib.request
from collections import defaultdict


def fetch(url, headers):
    return json.loads(urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=20).read())


def main():
    sid = os.environ.get("SID")
    key = os.environ.get("OPIK_KEY")
    ws = os.environ.get("OPIK_WORKSPACE", "comet-all")
    if not sid or not key:
        print("Set SID and OPIK_KEY", file=sys.stderr)
        sys.exit(2)
    H = {"Authorization": key, "Comet-Workspace": ws}

    # Latest trace for this thread
    filt = (
        '%5B%7B%22field%22%3A%22thread_id%22%2C%22operator%22%3A%22%3D%22'
        f'%2C%22value%22%3A%22{sid}%22%2C%22type%22%3A%22string_exact%22%7D%5D'
    )
    traces = fetch(
        f"https://www.comet.com/opik/api/v1/private/traces?project_name=claude-code&size=1&filters={filt}",
        H,
    )
    if not traces["content"]:
        print(f"no trace for thread_id={sid}", file=sys.stderr)
        sys.exit(1)
    latest = traces["content"][0]
    tid = latest["id"]
    print(f"trace {tid}")
    print(f"  input:  {(latest.get('input') or {}).get('text','')[:60]!r}")
    print(f"  spans:  {latest.get('span_count')}")

    # All spans
    spans = []
    for p in range(1, 10):
        d = fetch(
            f"https://www.comet.com/opik/api/v1/private/spans?project_name=claude-code&trace_id={tid}&size=200&page={p}",
            H,
        )
        rows = d.get("content", [])
        spans.extend(rows)
        if len(rows) < 200:
            break

    if not spans:
        print("FAIL: no spans on trace", file=sys.stderr)
        sys.exit(1)

    failures = []

    # B1 — required domains present. Session-scoped ones must always be there;
    # turn-scoped ones (thinking/tool_results/user_prompts/file_attachments/
    # assistant_text) are correctly nil when this turn has no such content yet.
    sample = next((s for s in spans if s.get("name") == "Bash"), spans[0])
    cc = sample.get("metadata", {}).get("cc", {})
    session_required = {"skills", "tools", "memory", "llm_call"}
    turn_optional = {
        "thinking", "tool_results", "user_prompts",
        "file_attachments", "prior_assistant", "assistant_text",
    }
    missing = session_required - set(cc.keys())
    if missing:
        failures.append(f"B1 ✗ session-scoped domains missing on {sample.get('name')}: {missing}")
    else:
        present_optional = turn_optional & set(cc.keys())
        print(f"  B1 ✓ all {len(session_required)} session domains present "
              f"+ {len(present_optional)}/{len(turn_optional)} turn-scoped on {sample.get('name')}")

    # H1/H2 — legacy gone
    if "context" in cc:
        failures.append("H1 ✗ legacy cc.context still present")
    if "attribution" in cc:
        failures.append("H2 ✗ legacy cc.attribution still present")
    if "context" not in cc and "attribution" not in cc:
        print("  H1/H2 ✓ no legacy keys")

    # H3 — no timestamps anywhere under cc
    found_ts = []

    def find_ts(o, path=""):
        if isinstance(o, dict):
            for k, v in o.items():
                lower = k.lower()
                if any(s in lower for s in ("time", "_at", "timestamp")) and lower not in ("start_time", "end_time"):
                    found_ts.append(f"{path}.{k}")
                find_ts(v, f"{path}.{k}")
        elif isinstance(o, list):
            for i, v in enumerate(o):
                find_ts(v, f"{path}[{i}]")

    find_ts(cc, "cc")
    if found_ts:
        failures.append(f"H3 ✗ timestamp-shaped fields under cc.*: {found_ts[:5]}")
    else:
        print("  H3 ✓ no timestamps under cc.*")

    # H4 — no legacy "Skill Loaded:" spans (those came from a pre-rewrite engine)
    legacy_spans = [s for s in spans if s.get("name", "").startswith(("Skill Loaded:", "Skill Invoked:"))]
    if legacy_spans:
        # Soft warning — historic traces have these, new traces should not
        print(f"  H4 ⚠ {len(legacy_spans)} legacy 'Skill Loaded:' spans (only OK on traces predating the rewrite)")

    # D3 — Σ attributed_output_tokens == anchor.usage.completion_tokens
    by_mid = defaultdict(list)
    for s in spans:
        llm = s.get("metadata", {}).get("cc", {}).get("llm_call", {})
        if llm.get("message_id"):
            by_mid[llm["message_id"]].append(s)
    mismatches = 0
    for mid, sps in by_mid.items():
        sps.sort(key=lambda x: x["metadata"]["cc"]["llm_call"]["block_index"])
        anchor = (sps[0].get("usage") or {}).get("completion_tokens", 0)
        if not anchor:
            continue
        attr = sum(s["metadata"]["cc"]["llm_call"].get("attributed_output_tokens", 0) for s in sps)
        if anchor != attr:
            mismatches += 1
            print(f"     Δ {mid[:24]}: anchor={anchor} Σattr={attr}")
    if mismatches:
        failures.append(f"D3 ✗ {mismatches} LLM calls' attribution off")
    else:
        print(f"  D3 ✓ all {sum(1 for v in by_mid.values() if (v[0].get('usage') or {}).get('completion_tokens', 0))} LLM calls' attribution sums to anchor.usage")

    # D4 — per-category breakdown is non-degenerate
    by_kind = defaultdict(int)
    for s in spans:
        llm = s.get("metadata", {}).get("cc", {}).get("llm_call", {})
        by_kind[llm.get("block_kind", "?")] += llm.get("attributed_output_tokens", 0)
    total = sum(by_kind.values())
    if total == 0:
        failures.append("D4 ✗ no output tokens attributed")
    else:
        print(f"  D4 ✓ per-category output token attribution:")
        for k, v in sorted(by_kind.items(), key=lambda kv: -kv[1]):
            print(f"      {k:<12} {v:>8}  ({v / total * 100:.1f}%)")
        # Sanity — thinking shouldn't be 100% (would mean the pre-rewrite bug is back)
        if "thinking" in by_kind and by_kind["thinking"] / total > 0.95:
            failures.append(f"D4 ⚠ thinking={by_kind['thinking']/total*100:.0f}% — looks like pre-rewrite over-attribution")

    # C7 — built-in + MCP == total available
    if "tools" in cc:
        t = cc["tools"]
        sm = t.get("summary", {})
        bs = sm.get("by_source", {})
        b = bs.get("builtin", {}).get("available_count", 0)
        m = bs.get("mcp", {}).get("available_count", 0)
        a = sm.get("available_count", 0)
        if b + m != a:
            failures.append(f"C7 ✗ {b} builtin + {m} mcp != {a} available")
        else:
            print(f"  C7 ✓ tools: {b} builtin + {m} mcp = {a} available")

    print()
    if failures:
        print("FAIL:")
        for f in failures:
            print(f"  {f}")
        sys.exit(1)
    print("✓ headline checks passed")


if __name__ == "__main__":
    main()
