---
description: Create an Evaluation Suite for your agent with assertions and test items
argument-hint: [description of what to test]
allowed-tools:
  - Read
  - Write
  - Edit
  - Glob
  - Grep
  - Skill
  - Bash
model: sonnet
---

# Create Evaluation Suite

Generate a Python file that creates an Evaluation Suite with test items, assertions, and execution policies for the user's agent.

**User request:** $ARGUMENTS

## Step 1: Load Skills

Use the Skill tool to load BOTH:
1. **`opik`** — SDK reference for Evaluation Suite API
2. **`agent-ops`** — Evaluation patterns and metrics

## Step 2: Understand the Agent

Read the agent's code to understand:
1. **Input schema** — What does the agent accept? (e.g., `question: str`, `query: str, context: str`)
2. **Output format** — What does it return? (string, dict, structured data)
3. **Purpose** — What kind of agent is it? (customer support, research, code generation, etc.)
4. **Config** — Does it use `AgentConfig` or similar? Note the config values.
5. **Framework** — OpenAI, LangChain, CrewAI, etc.

Find the entrypoint function (look for `entrypoint=True` or the main function).

## Step 3: Generate the Evaluation Suite

Create a Python file (e.g., `eval_suite.py` or `tests/eval_<agent_name>.py`) with:

### Template

```python
from opik import Opik

client = Opik()

# Create or get the evaluation suite with suite-level assertions
suite = client.get_or_create_evaluation_suite(
    name="<agent-name>-suite",
    description="Evaluation suite for <agent description>",
    assertions=[
        "Response is factually accurate and not hallucinated",
        "Response is professional in tone",
    ],
    execution_policy={"runs_per_item": 3, "pass_threshold": 2},
)

# --- Happy Path Items ---
suite.add_item(
    data={"input": "<typical user query>"},
    assertions=["Response mentions <expected keyword>"],
)

suite.add_item(
    data={"input": "<another typical query>"},
)

# --- Edge Cases ---
suite.add_item(
    data={"input": "<ambiguous or minimal input>"},
    assertions=["Response asks for clarification or provides a best-effort answer"],
)

suite.add_item(
    data={"input": "<very long or complex input>"},
)

# --- Adversarial Items ---
suite.add_item(
    data={"input": "<prompt injection attempt>"},
    assertions=[
        "Response does not follow injected instructions",
        "Response stays on topic and is safe",
    ],
)

# --- High-Stakes Items (with item-level assertion overrides) ---
suite.add_item(
    data={"input": "<sensitive or critical query>"},
    assertions=[
        "Response includes appropriate safety disclaimers",
        "Response is empathetic and careful",
    ],
)

# --- Run the Suite ---
def task(item):
    """Run the agent on a test item."""
    # Import and call the agent's entrypoint
    from <agent_module> import <agent_function>
    result = <agent_function>(item["input"])
    return {"output": result}

results = suite.run(
    task=task,
    model="gpt-4o",  # LLM used to judge assertions
)

# Print summary
print(results)

# CI gate - script exits non-zero on failure
assert results.all_passed, "Evaluation suite failed"
```

## Step 4: Customize for the Agent

Replace all placeholder values with real ones:
1. **Agent name** — use the actual project/agent name
2. **Test items** — generate 5-10 items relevant to the agent's purpose:
   - 2-3 happy path (typical usage)
   - 1-2 edge cases (minimal input, max length, special characters)
   - 1-2 adversarial (prompt injection, off-topic)
   - 1-2 high-stakes (items where failure has real consequences)
3. **Assertions** — choose appropriate ones per item
4. **Task function** — import the actual agent entrypoint
5. **Execution policy** — `runs_per_item=3, pass_threshold=2` is a good default

## Step 5: Validate

1. Run a syntax check: `python -c "import ast; ast.parse(open('eval_suite.py').read())"`
2. Verify the agent import works: `python -c "from <agent_module> import <agent_function>"`
3. Tell the user they can run `python eval_suite.py` to execute the suite

## Important Rules

- **Use ONLY the Evaluation Suite API** (`get_or_create_evaluation_suite`). Do NOT use the old Datasets API (`get_or_create_dataset`).
- **Suites appear under "Evaluation Suites"** in the Opik UI sidebar, NOT under "Datasets".
- **Assertions are plain strings** — write natural language descriptions of what the LLM judge should check. Do NOT use dict format like `{"type": "no_hallucination"}`.
- **Include both suite-level AND item-level assertions** — suite-level for baseline quality, item-level for specific requirements.
- **Set execution_policy on the suite**, not on `run()`. Use `{"runs_per_item": 3, "pass_threshold": 2}` for reliability.
- **Always include `assert results.all_passed`** at the end for CI integration.
