"""Calibrate chars/4 vs Anthropic count_tokens across content types.

Samples are pulled from real transcripts under
~/.claude/projects/-Users-collinc-code-opik/*.jsonl. Each sample is
classified into a content type and we record chars + actual tokens.
Output: per-type stats + a refined heuristic recommendation.
"""
from __future__ import annotations
import json
import os
import statistics
import sys
import time
from collections import defaultdict
from pathlib import Path

import anthropic

CHARS_PER_TOKEN_HEURISTIC = 4.0
PROJECTS = Path.home() / ".claude/projects/-Users-collinc-code-opik"
MODEL = "claude-sonnet-4-5"   # cheapest non-haiku; count_tokens is free

# Cap samples per type to avoid 10K API calls. 50/type is plenty for stats.
MAX_PER_TYPE = 150
# Cap text length per sample at 50K chars — count_tokens has a request
# size limit, and one outlier doesn't help statistics.
MAX_SAMPLE_CHARS = 50_000


def classify_text(text: str, hint: str) -> str:
    """Sort content into buckets so we can find per-type ratios."""
    if hint:
        return hint
    t = text.lstrip()
    if t.startswith("{") or t.startswith("["):
        return "json"
    if t.startswith("#") or "```" in text[:200]:
        return "markdown"
    if any(kw in text[:500] for kw in ("def ", "import ", "func ", "class ", "package ")):
        return "code"
    return "prose"


def collect_samples():
    samples = []  # (text, type)

    # Walk transcripts
    if not PROJECTS.exists():
        return samples
    for jsonl in sorted(PROJECTS.glob("*.jsonl"), key=lambda p: -p.stat().st_mtime)[:5]:
        try:
            with jsonl.open() as f:
                lines = f.readlines()
        except OSError:
            continue
        for raw in lines:
            try:
                e = json.loads(raw)
            except json.JSONDecodeError:
                continue
            t = e.get("type")
            msg = e.get("message") or {}

            # Attachments — skill bodies, deferred-tools, file attachments
            if t == "attachment":
                a = e.get("attachment") or {}
                at = a.get("type")
                if at == "skill_listing":
                    c = a.get("content")
                    if isinstance(c, str):
                        samples.append((c[:MAX_SAMPLE_CHARS], "skill_listing_menu"))
                elif at == "deferred_tools_delta":
                    raw_json = json.dumps(a)[:MAX_SAMPLE_CHARS]
                    samples.append((raw_json, "deferred_tools_payload"))

            elif t == "user":
                content = msg.get("content")
                # Plain string: user prompt
                if isinstance(content, str):
                    samples.append((content[:MAX_SAMPLE_CHARS], "user_prompt"))
                elif isinstance(content, list):
                    for c in content:
                        if not isinstance(c, dict):
                            continue
                        ctype = c.get("type")
                        if ctype == "tool_result":
                            inner = c.get("content")
                            if isinstance(inner, str):
                                samples.append((inner[:MAX_SAMPLE_CHARS], "tool_result"))
                            elif isinstance(inner, list):
                                bits = [b.get("text","") for b in inner if isinstance(b, dict)]
                                joined = "\n".join(bits)[:MAX_SAMPLE_CHARS]
                                if joined:
                                    samples.append((joined, "tool_result"))
                        elif ctype == "text":
                            # Often skill bodies arrive as user text after a Skill tool_use
                            text = c.get("text","")
                            if text:
                                samples.append((text[:MAX_SAMPLE_CHARS],
                                                classify_text(text, "skill_body" if "Base directory for this skill" in text[:200] else "")))

            elif t == "assistant":
                content = msg.get("content")
                if isinstance(content, list):
                    for c in content:
                        if not isinstance(c, dict):
                            continue
                        ctype = c.get("type")
                        if ctype == "text":
                            text = c.get("text","")
                            if text:
                                samples.append((text[:MAX_SAMPLE_CHARS], "assistant_text"))
                        elif ctype == "tool_use":
                            inp = c.get("input") or {}
                            raw = json.dumps(inp)[:MAX_SAMPLE_CHARS]
                            if raw and raw != "{}":
                                samples.append((raw, "tool_use_input"))

    # Dedupe identical strings (same skill body loaded many times)
    seen = set()
    uniq = []
    for text, t in samples:
        if text in seen or not text.strip():
            continue
        seen.add(text)
        uniq.append((text, t))
    return uniq


def main():
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        print("ANTHROPIC_API_KEY not set", file=sys.stderr)
        sys.exit(1)

    client = anthropic.Anthropic(api_key=api_key)
    samples = collect_samples()
    print(f"collected {len(samples)} unique samples across {len(set(t for _,t in samples))} types", file=sys.stderr)

    # Group by type, cap per type
    by_type: dict[str, list[str]] = defaultdict(list)
    for text, t in samples:
        if len(by_type[t]) < MAX_PER_TYPE:
            by_type[t].append(text)

    # Also include a couple of synthetic samples for known content types
    synthetic = {
        "very_short_prose": ["hello world", "hi", "thanks!", "what time is it?"],
        "very_short_json":  ['{"a":1}', '{"file":"foo.txt"}', '[]', 'null'],
    }
    for t, texts in synthetic.items():
        by_type[t].extend(texts)

    print(f"\nCalling count_tokens on {sum(len(v) for v in by_type.values())} samples...", file=sys.stderr)

    rows = []
    for ctype, texts in by_type.items():
        for i, text in enumerate(texts):
            try:
                r = client.messages.count_tokens(
                    model=MODEL,
                    messages=[{"role": "user", "content": text}],
                )
                actual_tokens = r.input_tokens
            except anthropic.BadRequestError as e:
                print(f"  skip {ctype}[{i}]: {e}", file=sys.stderr)
                continue
            chars = len(text)
            # Subtract a constant per request for the message wrapper overhead.
            # We'll estimate this from many small samples; for now keep raw count.
            rows.append({
                "type": ctype,
                "chars": chars,
                "actual": actual_tokens,
                "heuristic_4": int(chars/4) or 1,
            })
            # rate limit: at most ~50 req/s
            time.sleep(0.02)

    # ---- Stats ----
    # First, estimate the per-request wrapper overhead using very small samples
    short_rows = [r for r in rows if r["chars"] <= 20]
    if short_rows:
        wrapper = round(statistics.median(r["actual"] - max(1, r["chars"]//4) for r in short_rows))
        wrapper = max(0, wrapper)
    else:
        wrapper = 0
    print(f"\nestimated wrapper overhead per count_tokens request: {wrapper} tokens", file=sys.stderr)

    for r in rows:
        # Approximate content-only tokens by subtracting wrapper
        r["content_tokens"] = max(1, r["actual"] - wrapper)
        r["chars_per_token"] = r["chars"] / r["content_tokens"]

    by_type_stats: dict[str, dict] = {}
    for r in rows:
        by_type_stats.setdefault(r["type"], {"chars":[], "actual":[], "content":[], "ratio":[]})
        s = by_type_stats[r["type"]]
        s["chars"].append(r["chars"])
        s["actual"].append(r["actual"])
        s["content"].append(r["content_tokens"])
        s["ratio"].append(r["chars_per_token"])

    print(f"\n{'type':<28} {'n':>4} {'median chars/tok':>18} {'mean':>8} {'p10':>8} {'p90':>8}")
    print("-" * 90)
    for t in sorted(by_type_stats):
        s = by_type_stats[t]
        ratios = sorted(s["ratio"])
        med = statistics.median(ratios)
        mean = statistics.mean(ratios)
        p10 = ratios[len(ratios)//10] if len(ratios)>=10 else ratios[0]
        p90 = ratios[(9*len(ratios))//10] if len(ratios)>=10 else ratios[-1]
        print(f"{t:<28} {len(ratios):>4} {med:>18.3f} {mean:>8.3f} {p10:>8.3f} {p90:>8.3f}")

    # Overall
    all_ratios = [r["chars_per_token"] for r in rows]
    overall_med = statistics.median(all_ratios)
    overall_mean = statistics.mean(all_ratios)
    print(f"\nOverall median chars/token: {overall_med:.3f}")
    print(f"Overall mean   chars/token: {overall_mean:.3f}")
    print(f"Current heuristic (4.0): mean error vs actual = {statistics.mean(abs(r['heuristic_4']-r['content_tokens'])/r['content_tokens']*100 for r in rows):.1f}%")

    # Save raw data
    out = Path("/tmp/calibrate_results.json")
    out.write_text(json.dumps({
        "model": MODEL,
        "wrapper_overhead": wrapper,
        "rows": rows,
        "by_type": {t: {
            "n": len(s["chars"]),
            "median_chars_per_token": statistics.median(s["ratio"]),
            "mean_chars_per_token": statistics.mean(s["ratio"]),
            "p10_chars_per_token": sorted(s["ratio"])[len(s["ratio"])//10] if len(s["ratio"])>=10 else sorted(s["ratio"])[0],
            "p90_chars_per_token": sorted(s["ratio"])[(9*len(s["ratio"]))//10] if len(s["ratio"])>=10 else sorted(s["ratio"])[-1],
        } for t,s in by_type_stats.items()},
    }, indent=2))
    print(f"\nraw results: {out}")


if __name__ == "__main__":
    main()
