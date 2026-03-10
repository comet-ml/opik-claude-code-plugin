---
name: opik
description: This skill should be used when the user needs to add Opik tracing or integrations to their code, instrument an LLM application, or needs reference for Opik SDK usage (Python, TypeScript, REST API). Use for tasks like "add tracing", "instrument my code", "use track_openai", "add OpikTracer", "what span types are available", "how to flush traces".
---

# Opik SDK Reference

Opik is an open-source LLM observability platform. This skill covers the SDK: tracing, integrations, span types, and how to instrument code.

## Core Concepts

### Traces and Spans

A **trace** is a complete execution path (one user request → one response). **Spans** are individual operations within a trace, forming a hierarchy.

### Span Types

| Type | Use For | Example |
|------|---------|---------|
| `general` | Custom operations, orchestration | Data processing, agent entry point |
| `llm` | LLM API calls | OpenAI completion, Anthropic message |
| `tool` | Tool/function execution, data retrieval | Web search, vector DB query, calculator |
| `guardrail` | Safety/validation checks | PII detection, content moderation |

**These are the ONLY valid span types.** Do NOT use `retrieval` or any other type.

## Python Quick Start

```python
import opik

@opik.track(name="my_agent", type="general")
def agent(query: str) -> str:
    context = retrieve(query)
    return generate(query, context)

@opik.track(type="tool")
def retrieve(query: str) -> list:
    return search_db(query)

@opik.track(type="llm")
def generate(query: str, context: list) -> str:
    return llm_call(query, context)

# Nested calls automatically create child spans
result = agent("What is ML?")
opik.flush_tracker()  # Flush for scripts
```

## TypeScript Quick Start

```typescript
import { Opik } from "opik";

const client = new Opik({ projectName: "my-project" });

const trace = client.trace({ name: "my-agent", input: { query: "Hello" } });
const span = trace.span({ name: "llm-call", type: "llm" });
// ... LLM call
span.end({ output: { response: "Hi!" } });
trace.end({ output: { response: "Hi!" } });

await client.flush();
```

## Framework Integrations

Use framework-specific integrations instead of manual `@opik.track` when available — they capture more detail (tokens, model, cost) automatically.

For the full list of integrations with code snippets, see `references/integrations.md`.

### Common Patterns

**Wrap-the-client** (OpenAI, Anthropic, Bedrock, Gemini, etc.):
```python
from opik.integrations.openai import track_openai
client = track_openai(OpenAI())
# All calls now traced automatically
```

**Global enable** (CrewAI, DSPy, etc.):
```python
from opik.integrations.crewai import track_crewai
track_crewai(project_name="my-project", crew=crew)  # crew= required for v1.0.0+
```

**Callback-based** (DSPy):
```python
from opik.integrations.dspy import OpikCallback
dspy.configure(callbacks=[OpikCallback()])
```

**Callback/tracer** (LangChain, LangGraph, LlamaIndex):
```python
from opik.integrations.langchain import OpikTracer
tracer = OpikTracer()
result = chain.invoke(input, config={"callbacks": [tracer]})
```

**Agent-specific** (Google ADK):
```python
from opik.integrations.adk import OpikTracer, track_adk_agent_recursive
opik_tracer = OpikTracer()
track_adk_agent_recursive(agent, opik_tracer)
```


## Detailed References

| Topic | Reference File |
|-------|----------------|
| Python SDK (decorators, context, async, distributed tracing) | `references/tracing-python.md` |
| TypeScript SDK (client, decorators, framework integrations) | `references/tracing-typescript.md` |
| REST API (HTTP endpoints, authentication) | `references/tracing-rest-api.md` |
| All integrations with code snippets | `references/integrations.md` |
| Core concepts (traces, spans, threads, metadata, feedback) | `references/observability.md` |
