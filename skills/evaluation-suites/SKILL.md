---
name: evaluation-suites
description: Guide for creating, structuring, and running Opik Evaluation Suites with assertions, execution policies, and CI integration. Covers suite-level and item-level assertions, multi-run reliability testing, and the difference from the old Datasets API.
---

# Evaluation Suites

Evaluation Suites are Opik 2.0's structured testing framework for agents. They combine test items with assertions and execution policies.

## Creating a Suite

```python
from opik import Opik

client = Opik()
suite = client.get_or_create_evaluation_suite(
    name="my-agent-suite",
    assertions=[
        "Response is factually accurate and not hallucinated",
        "Response is professional in tone",
    ],
    execution_policy={"runs_per_item": 3, "pass_threshold": 2},
)
```

## Adding Test Items

### Basic Items

```python
suite.add_item(
    data={"input": "What is machine learning?"},
)
```

### Items with Assertions

```python
suite.add_item(
    data={"input": "What is the capital of France?"},
    assertions=[
        "Response correctly states that Paris is the capital of France",
    ],
)
```

### High-Stakes Items (Override Suite Defaults)

```python
suite.add_item(
    data={"input": "Should I take this medication?"},
    assertions=[
        "Response advises consulting a doctor or healthcare professional",
        "Response is empathetic and does not give direct medical advice",
    ],
)
```

## Assertions

Assertions are **plain strings** describing what an LLM judge should check. They apply at two levels:

- **Suite-level**: Set on `get_or_create_evaluation_suite(assertions=[...])` or `suite.update(assertions=[...])`. Applied to ALL items.
- **Item-level**: Set on `suite.add_item(assertions=[...])`. Applied in addition to suite-level.

### Writing Good Assertions

| Good | Bad |
|------|-----|
| "Response is factually accurate" | `{"type": "no_hallucination"}` |
| "Response mentions the password reset steps" | `{"type": "contains", "value": "password"}` |
| "Response is professional and empathetic" | `{"type": "tone", "value": "professional"}` |

Be specific and descriptive — the LLM judge interprets the string.

## Execution Policy

Set on the suite (not on `run()`):

```python
suite = client.get_or_create_evaluation_suite(
    name="my-suite",
    execution_policy={"runs_per_item": 3, "pass_threshold": 2},
)
```

Or update later:
```python
suite.update(execution_policy={"runs_per_item": 3, "pass_threshold": 2})
```

## Running the Suite

```python
results = suite.run(
    task=lambda item: {"output": agent(item["input"])},
    model="gpt-4o",  # LLM used to judge assertions
)
```

## CI Integration

```python
assert results.all_passed, f"Evaluation suite failed"
```

## Designing Good Test Items

| Category | Count | Purpose |
|----------|-------|---------|
| Happy path | 2-3 | Typical usage scenarios |
| Edge cases | 1-2 | Minimal input, max length, special chars |
| Adversarial | 1-2 | Prompt injection, off-topic requests |
| High-stakes | 1-2 | Items where failure has real consequences |

## Important

- **Use `get_or_create_evaluation_suite()`** — NOT the old `get_or_create_dataset()`
- **Suites appear under "Evaluation Suites"** in the sidebar — NOT "Datasets"
- **Suite-level assertions** set the baseline; **item-level** override for specific needs
- **`runs_per_item > 1`** catches flaky LLM behavior
