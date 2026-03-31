---
name: agent-ops
description: This skill should be used when the user asks about agent architecture, evaluation, metrics, production monitoring, debugging agents, or best practices for building reliable AI agents. Use for questions like "evaluate my agent", "set up production monitoring", "add guardrails", "detect hallucinations", "agent anti-patterns", "compare experiments", "create evaluation dataset".
---

# Agent Operations: Build, Evaluate, and Monitor AI Agents

This skill covers the agent lifecycle beyond basic tracing: architecture patterns, evaluation, metrics, and production monitoring. All examples use Opik for observability — for SDK details (tracing, integrations, span types), load the `opik` skill.

## The Agent Lifecycle

1. **Instrument** — Add Opik tracing to make your agent's behavior visible (see `opik` skill)
2. **Evaluate** — Measure performance with datasets, metrics, and experiments
3. **Monitor** — Track quality, cost, and reliability in production
4. **Optimize** — Improve based on data from evaluation and production traces

## Agent Architecture Patterns

Trace every component of your agent with appropriate span types:

```python
import opik

@opik.track(name="research_agent")
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
| Planning | `general` | Reasoning steps, decisions |
| Tool calls | `tool` | Tool name, parameters, results |
| LLM calls | `llm` | Prompt, response, tokens |
| Retrieval | `tool` | Query, documents |
| Validation | `guardrail` | Check results, pass/fail |

## Evaluation

Evaluate agents at multiple levels — end-to-end and per-component:

```python
from opik.evaluation import evaluate
from opik.evaluation.metrics import AnswerRelevance, Hallucination, AgentTaskCompletion

results = evaluate(
    experiment_name="agent-v2",
    dataset=dataset,
    task=lambda item: {"output": agent(item["input"])},
    scoring_metrics=[
        AnswerRelevance(),
        Hallucination(),
        AgentTaskCompletion(),
    ]
)
```

### Built-in Agent Metrics

| Metric | What It Measures |
|--------|-----------------|
| `AgentTaskCompletion` | Did the agent fulfill its task? |
| `AgentToolCorrectness` | Were tools used correctly? |
| `TrajectoryAccuracy` | Did actions match expected sequence? |
| `AnswerRelevance` | Does the answer address the question? |
| `Hallucination` | Are there unsupported claims? |

### 41 Total Built-in Metrics

Heuristic (Equals, Contains, BLEU, ROUGE, BERTScore, IsJson, etc.), LLM-as-Judge (AnswerRelevance, Hallucination, Usefulness, GEval, etc.), RAG (ContextPrecision, ContextRecall, Faithfulness), and conversation metrics. See `references/evaluation.md` for the full list.

## Production Monitoring

- **Dashboards** — Visualize quality, cost, latency, and error trends
- **Online evaluation** — Automatically score production traces with LLM-as-Judge
- **Alerts** — Get notified when metrics deviate (quality drops, cost spikes, error rates)
- **Guardrails** — PII detection, topic validation, custom safety checks
- **Opik Assist** — AI-powered root cause analysis for failed traces

## Common Anti-Patterns

| Category | Anti-Pattern |
|----------|-------------|
| Reliability | Unbounded loops, retry storms, silent failures |
| Security | Prompt injection, privilege escalation, data leakage |
| Observability | Late tracing (missing input), orphaned spans |
| Tools | Tool loops, hallucinated tools, parameter errors |

## Detailed References

| Topic | Reference File |
|-------|----------------|
| Agent architecture, reliability, security patterns | `references/agent-patterns.md` |
| Evaluation datasets, experiments, all 41 metrics | `references/evaluation.md` |
| Production dashboards, alerts, guardrails, cost tracking | `references/production.md` |
