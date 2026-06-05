"""Replay the calibration dataset against the NEW per-type heuristic."""
import json
import statistics
from pathlib import Path

raw = json.loads(Path("/tmp/calibrate_results.json").read_text())
wrapper = raw["wrapper_overhead"]
rows = raw["rows"]


def chars_per_token(text: str, content_type: str) -> float:
    # Auto-detect JSON when type is empty
    if content_type == "":
        for c in text:
            if c in " \t\n\r":
                continue
            if c in "{[":
                content_type = "json"
            break
    if content_type in ("json", "tool_use_input"):
        return 2.8
    if content_type == "deferred_tools_payload":
        return 2.5
    if content_type == "tool_result":
        return 3.0
    if content_type == "skill_body":
        return 3.5
    if content_type in ("assistant_text", "prose", "skill_listing_menu", "markdown"):
        return 3.9
    if content_type == "user_prompt":
        return 4.3
    return 3.6


def tok_estimate(text: str, content_type: str = "") -> int:
    if not text:
        return 0
    n = int(len(text) / chars_per_token(text, content_type))
    return n if n > 0 else 1


# Map calibration type → engine content_type hint
TYPE_HINT = {
    "tool_use_input":           "tool_use_input",
    "tool_result":              "tool_result",
    "deferred_tools_payload":   "deferred_tools_payload",
    "skill_body":               "skill_body",
    "skill_listing_menu":       "skill_listing_menu",
    "assistant_text":           "assistant_text",
    "user_prompt":              "user_prompt",
    "markdown":                 "prose",       # close enough
    "prose":                    "prose",
    "json":                     "json",
    "very_short_prose":         "user_prompt",
    "very_short_json":          "json",
}


# Compute errors with each heuristic
old_errors = []
new_errors = []
per_type_old = {}
per_type_new = {}

for r in rows:
    actual = r["content_tokens"]
    if actual <= 0:
        continue
    hint = TYPE_HINT.get(r["type"], "")
    old_est = r["heuristic_4"]
    new_est = tok_estimate("x" * r["chars"], hint)  # synthesize a body of that length
    # ^ That uses the divisor but loses auto-detect. Apply hint explicitly.
    old_err = abs(old_est - actual) / actual
    new_err = abs(new_est - actual) / actual
    old_errors.append(old_err)
    new_errors.append(new_err)
    per_type_old.setdefault(r["type"], []).append(old_err)
    per_type_new.setdefault(r["type"], []).append(new_err)


def pct(x):
    return f"{x*100:5.1f}%"


print(f"{'type':<28} {'n':>4} {'old mean err':>14} {'new mean err':>14} {'old median':>12} {'new median':>12}")
print("-" * 90)
for t in sorted(per_type_old):
    o = per_type_old[t]
    n = per_type_new[t]
    print(f"{t:<28} {len(o):>4} {pct(statistics.mean(o)):>14} {pct(statistics.mean(n)):>14} "
          f"{pct(statistics.median(o)):>12} {pct(statistics.median(n)):>12}")

print(f"\nOverall mean error:    old={pct(statistics.mean(old_errors))}  new={pct(statistics.mean(new_errors))}")
print(f"Overall median error:  old={pct(statistics.median(old_errors))}  new={pct(statistics.median(new_errors))}")
print(f"\nWorst-case (p95) error: old={pct(sorted(old_errors)[int(len(old_errors)*0.95)])}  new={pct(sorted(new_errors)[int(len(new_errors)*0.95)])}")
