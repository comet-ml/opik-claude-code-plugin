---
name: agent-ops
description: This skill should be used when the user asks about agent architecture, evaluation, metrics, production monitoring, debugging agents, best practices for building reliable AI agents, agent configuration, Blueprints, Evaluation Suites, opik connect, Local Runner, thread tracking, or conversation metrics. Use for questions like "evaluate my agent", "set up production monitoring", "add guardrails", "detect hallucinations", "agent anti-patterns", "compare experiments", "create evaluation suite", "configure my agent", "connect my agent", "track conversations", "evaluate threads".
---

# Agent Operations: Build, Evaluate, and Monitor AI Agents

This skill covers the agent lifecycle beyond basic tracing: architecture patterns, configuration, evaluation, threads, and production monitoring. All examples use Opik for observability — for SDK details (tracing, integrations, span types), load the `opik` skill.

## The Agent Lifecycle (Opik 2.0)

1. **Instrument** — Add `@opik.track` + `opik.AgentConfig` + `entrypoint=True` (see `opik` skill)
2. **Configure** — Externalize config into a dataclass. Opik manages Blueprints (immutable config snapshots) with environment tags (DEV/PROD)
3. **Connect** — Use `opik connect` to pair the Local Runner so the agent can be triggered from the Opik UI
4. **Evaluate** — Create Evaluation Suites with assertions and execution policies
5. **Monitor** — Track quality, cost, and reliability in production dashboards
6. **Optimize** — Use MaskIDs to test config variations, evaluate with suites, promote winning Blueprints

## Agent Configuration (Blueprints)

Opik 2.0 introduces **Agent Configuration** — externalized, version-controlled config for agents.

### Key Concepts

- **`opik.AgentConfig`** — Base class for config definitions. Subclass it with typed fields.
- **Blueprint** — An immutable snapshot of a config version. Every config edit creates a new Blueprint.
- **Environment Tags** — Labels like `DEV`, `STAGING`, `PROD` that point to specific Blueprints.
- **MaskID** — A temporary override layer for A/B testing config variations.
- **`entrypoint=True`** — Marks the main function so Opik can trigger the agent via the Local Runner.

### Config Pattern

```python
from typing import Annotated
import opik

class AgentConfig(opik.AgentConfig):
    model: Annotated[str, "LLM model to use"]
    temperature: Annotated[float, "Sampling temperature"]
    system_prompt: Annotated[str, "System prompt for the agent"]
    max_tokens: Annotated[int, "Maximum tokens in response"]

config = AgentConfig(
    model="gpt-4o",
    temperature=0.7,
    system_prompt="You are a helpful assistant.",
    max_tokens=1024,
)

@opik.track(entrypoint=True, project_name="my-agent")
def run_agent(question: str) -> str:
    """Run the agent with a user question.

    Args:
        question: The user's question to answer.
    """
    response = client.chat.completions.create(
        model=config.model,
        messages=[{"role": "system", "content": config.system_prompt},
                  {"role": "user", "content": question}],
        temperature=config.temperature,
        max_tokens=config.max_tokens,
    )
    return response.choices[0].message.content

# Publish config to Opik for Blueprint management
opik_client = opik.Opik()
opik_client.create_agent_config_version(config, project_name="my-agent")
```

### Environment Tags Workflow

1. Developer edits config → new Blueprint created automatically
2. `DEV` tag moves to new Blueprint
3. Test with Evaluation Suite → passes
4. Promote: move `PROD` tag to the new Blueprint
5. Production agent reads `PROD` Blueprint on next invocation

## Opik Connect (Local Runner)

The **Local Runner** lets you trigger your agent from the Opik browser UI while it runs on your local machine.

### Setup Flow

1. Instrument with `entrypoint=True` (required)
2. Add a docstring with argument descriptions to the entrypoint (required for schema discovery)
3. Run `opik connect` (Cloud) or `opik connect --pair <CODE>` (OSS)
4. Agent appears in Opik UI → type input → click Run → executes locally

### What the Runner Enables

- **UI-triggered execution** — Test your agent from the browser
- **Trace replay** — Click "Re-run" on any trace to replay with same input
- **Config iteration** — Edit config in UI → re-run → compare traces
- **Parallel jobs** — Runner handles concurrent executions

## Thread Tracking (Multi-Turn Conversations)

For conversational agents, group related traces into **threads** using `thread_id`.

### How Threads Work

- Each conversation turn = one trace
- All traces sharing a `thread_id` form a thread
- Threads tab shows: `first_message`, `last_message`, `number_of_messages`, `duration`, `total_estimated_cost`

### Instrumenting Conversational Agents

```python
import opik

@opik.track(entrypoint=True, project_name="chat-agent")
def handle_message(session_id: str, message: str) -> str:
    """Handle a chat message in a conversation session.

    Args:
        session_id: The conversation session identifier.
        message: The user's message.
    """
    opik.update_current_trace(thread_id=session_id)
    response = generate_response(session_id, message)
    return response
```

### Conversation Thread Metrics

Evaluate entire conversations, not just individual turns:

```python
from opik.evaluation import evaluate_threads
from opik.evaluation.metrics.conversation import (
    SessionCompletenessMetric,
    UserFrustrationMetric,
    ConversationalCoherenceMetric,
)

results = evaluate_threads(
    project_name="chat-agent",
    metrics=[
        SessionCompletenessMetric(),
        UserFrustrationMetric(),
        ConversationalCoherenceMetric(),
    ],
)
```

## Evaluation Suites

**Evaluation Suites** replace the old "Datasets" approach with a structured testing framework that includes assertions and execution policies.

### Creating a Suite

```python
from opik import Opik

client = Opik()
suite = client.get_or_create_evaluation_suite(
    name="customer-support-suite",
    assertions=[
        "Response is factually accurate and not hallucinated",
        "Response is professional in tone",
    ],
    execution_policy={"runs_per_item": 3, "pass_threshold": 2},
)

suite.add_item(
    data={"input": "How do I reset my password?"},
    assertions=["Response mentions the password reset process"],
)

suite.add_item(
    data={"input": "I want to cancel my account"},
    assertions=[
        "Response acknowledges the cancellation request",
        "Response is empathetic and offers alternatives",
    ],
)
```

### Suite-Level vs Item-Level Assertions

- **Suite-level**: Set via `assertions=` on `get_or_create_evaluation_suite()` or `suite.update(assertions=[...])`. Applied to ALL items.
- **Item-level**: Set via `assertions=` on `suite.add_item()`. Applied only to that item (in addition to suite-level).
- Assertions are **plain strings** describing what the LLM judge should check.

### Execution Policy

Set on the suite, not on `run()`:

```python
# Execution policy is set at suite creation or via update():
suite.update(execution_policy={"runs_per_item": 3, "pass_threshold": 2})

# Run the suite — model param tells which LLM judges assertions
results = suite.run(
    task=lambda item: {"output": agent(item["input"])},
    model="gpt-4o",  # LLM used to judge assertions
)

assert results.all_passed  # CI gate
```

### Suites in the UI

Evaluation Suites appear under **"Evaluation Suites"** in the sidebar — NOT under "Datasets".

## Architecture Patterns

Trace every component with appropriate span types:

```python
import opik

@opik.track(entrypoint=True, name="research_agent")
def agent(query: str) -> str:
    plan = plan_action(query)        # general span
    results = execute_tool(plan)     # tool span
    return generate_response(results) # llm span

@opik.track(type="tool")
def execute_tool(action: dict) -> str:
    return search_web(action["query"])

@opik.track(type="llm")
def generate_response(context: str) -> str:
    return llm_call(context)
```

### What to Trace

| Component | Span Type | Key Data |
|-----------|-----------|----------|
| Entry point | `general` | `entrypoint=True`, full input |
| Planning | `general` | Reasoning steps, decisions |
| Tool calls | `tool` | Tool name, parameters, results |
| LLM calls | `llm` | Prompt, response, tokens |
| Retrieval | `tool` | Query, documents |
| Validation | `guardrail` | Check results, pass/fail |

### Built-in Agent Metrics

| Metric | What It Measures |
|--------|-----------------|
| `AgentTaskCompletion` | Did the agent fulfill its task? |
| `AgentToolCorrectness` | Were tools used correctly? |
| `TrajectoryAccuracy` | Did actions match expected sequence? |
| `AnswerRelevance` | Does the answer address the question? |
| `Hallucination` | Are there unsupported claims? |

41 total built-in metrics: heuristic, LLM-as-Judge, RAG, conversation, and agent-specific. See `references/evaluation.md` for the full list.

## Production Monitoring

- **Dashboards** — Visualize quality, cost, latency, and error trends (including thread-level metrics)
- **Online evaluation** — Automatically score production traces with LLM-as-Judge
- **Alerts** — Get notified when metrics deviate (quality drops, cost spikes, error rates)
- **Guardrails** — PII detection, topic validation, custom safety checks
- **Opik Assist** — AI-powered root cause analysis for failed traces
- **Blueprint tracking** — See which config version each trace used

## Common Anti-Patterns

| Category | Anti-Pattern |
|----------|-------------|
| Configuration | Hardcoded model/temperature/prompt values instead of `opik.AgentConfig` |
| Entrypoint | Missing `entrypoint=True` — agent can't be triggered via Local Runner |
| Threads | Conversational agent without `thread_id` — turns appear as unrelated traces |
| Evaluation | Using old Datasets API instead of Evaluation Suites |
| Reliability | Unbounded loops, retry storms, silent failures |
| Security | Prompt injection, privilege escalation, data leakage |
| Observability | Late tracing (missing input), orphaned spans |
| Tools | Tool loops, hallucinated tools, parameter errors |

## Detailed References

| Topic | Reference File |
|-------|----------------|
| Agent architecture, reliability, security patterns | `references/agent-patterns.md` |
| Evaluation Suites, datasets, experiments, all 41 metrics | `references/evaluation.md` |
| Production dashboards, alerts, guardrails, cost tracking, Blueprints | `references/production.md` |
