# Token estimator calibration

Calibration of the engine's `tokEstimate` against Anthropic's
`messages.count_tokens` API. The goal is **good estimates without calling
the API at runtime** — count_tokens runs only here, in this offline
calibration step.

## How it works

1. `calibrate_tokenizer.py` walks the user's recent Claude Code transcripts
   under `~/.claude/projects/<workspace>/*.jsonl` and pulls samples of every
   content type that flows through the engine:

   | sample type             | source                                                      |
   | ----------------------- | ----------------------------------------------------------- |
   | `tool_use_input`        | assistant `tool_use` blocks, `input` JSON                   |
   | `tool_result`           | user `tool_result` blocks, `content` text/array             |
   | `deferred_tools_payload`| `attachment.type == deferred_tools_delta` raw JSON           |
   | `skill_listing_menu`    | `attachment.type == skill_listing.content`                  |
   | `skill_body`            | user text right after a Skill tool_use (the actual body)    |
   | `assistant_text`        | assistant `text` blocks                                     |
   | `user_prompt`           | user text content (plain string form)                       |
   | `json` / `markdown` / `prose` / `code` | auto-classified                              |

2. For each sample, call `count_tokens` to get the real token count.
3. Compute `chars / actual_tokens` per sample, take the per-type median.
4. `evaluate_heuristic.py` replays the dataset under the new per-type
   heuristic in `src/attribution.go` and reports the improvement.

## Findings (643-sample run)

Calibrated per-type chars/token ratios:

| content type             | n   | median chars/tok |
| ------------------------ | --- | ---------------- |
| deferred_tools_payload   | 5   | 2.52             |
| tool_use_input           | 150 | 2.83             |
| tool_result              | 150 | 2.99             |
| skill_body               | 6   | 3.49             |
| markdown                 | 3   | 3.77             |
| assistant_text           | 150 | 3.88             |
| skill_listing_menu       | 4   | 3.94             |
| prose                    | 2   | 3.83             |
| user_prompt              | 150 | 4.33             |
| **overall median**       | —   | **3.57**         |

JSON-shaped content (tool inputs, MCP payloads, deferred-tool lists) is
**~2.8 chars/token** — meaningfully denser than the historic chars/4
assumption (which is closer to user-prompt natural English).

## Old vs new heuristic accuracy

Mean abs error vs `count_tokens` on 643 samples:

| metric                | chars/4 | per-type heuristic |
| --------------------- | ------- | ------------------ |
| mean error            | 20.5%   | 15.3%              |
| **median error**      | 19.3%   | **9.9%**           |
| p95 worst case        | 44.3%   | 50.0%              |

The median error is cut in half. The mean is dragged up by some outlier
short strings (large %, small absolute). For lane-total attribution the
median is the meaningful number.

The biggest absolute wins:

| content type            | old error | new error |
| ----------------------- | --------- | --------- |
| deferred_tools_payload  | 34.5%     | **6.3%**  |
| tool_use_input          | 27.5%     | **12.6%** |
| tool_result             | 26.2%     | **22.6%** |
| skill_body              | 12.3%     | **7.3%**  |

## Refreshing the calibration

```bash
python3 -m venv venv && source venv/bin/activate
pip install anthropic
export ANTHROPIC_API_KEY=...
python calibrate_tokenizer.py            # produces /tmp/calibrate_results.json
python evaluate_heuristic.py             # reports old vs new error vs the Go heuristic
```

If you change the per-type ratios in `src/attribution.go::charsPerToken`,
re-run `evaluate_heuristic.py` to confirm you haven't regressed.

## Why we don't call count_tokens at runtime

- Adds a network call per estimation — kills the inline hook latency budget.
- Adds an API quota dependency for an engine that should run anywhere.
- Per-type heuristic is within ~10% median error, which is fine for the
  proportional / lane-total attribution the dashboard uses. Absolute USD
  numbers would still be approximate either way (Opus 4.7 input/output
  prices, prompt-caching discounts, etc. all introduce ±5–10% noise of
  their own).
