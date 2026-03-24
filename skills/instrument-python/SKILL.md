---
name: instrument-python
description: Step-by-step guide for adding Opik observability to Python LLM applications. Covers @opik.track decorators, opik.AgentConfig for configuration externalization, entrypoint=True for Local Runner, and thread_id for conversational agents.
---

# Instrument Python Agents with Opik

Step-by-step guide to making your Python agent observable with Opik 2.0.

## Quick Start (3 Steps)

### 1. Add @opik.track to your functions

```python
import opik

@opik.track(entrypoint=True, name="my-agent")
def agent(query: str) -> str:
    """Run the agent.

    Args:
        query: The user's question.
    """
    context = retrieve(query)
    return generate(query, context)

@opik.track(type="tool")
def retrieve(query: str) -> list:
    return search_db(query)

@opik.track(type="llm")
def generate(query: str, context: list) -> str:
    return llm_call(query, context)
```

### 2. Extract config into an AgentConfig

```python
from typing import Annotated
import opik

class AgentConfig(opik.AgentConfig):
    model: Annotated[str, "LLM model to use"]
    temperature: Annotated[float, "Sampling temperature"]
    system_prompt: Annotated[str, "System prompt"]
    max_tokens: Annotated[int, "Maximum tokens"]

config = AgentConfig(
    model="gpt-4o",
    temperature=0.7,
    system_prompt="You are a helpful assistant.",
    max_tokens=1024,
)
```

### 3. Set up the project

```bash
pip install opik
opik configure  # Set API key and URL
export OPIK_PROJECT_NAME="my-agent"
```

## Span Types

| Type | Use | Example |
|------|-----|---------|
| `general` | Orchestration, entry points | Agent main function |
| `llm` | LLM API calls | OpenAI completion |
| `tool` | Tool execution, retrieval | Web search, DB query |
| `guardrail` | Safety checks | PII detection |

## Framework Integrations

### OpenAI
```python
from opik.integrations.openai import track_openai
client = track_openai(OpenAI())
```

### LangChain / LangGraph
```python
from opik.integrations.langchain import OpikTracer
tracer = OpikTracer()
result = chain.invoke(input, config={"callbacks": [tracer]})
```

### Anthropic
```python
from opik.integrations.anthropic import track_anthropic
client = track_anthropic(anthropic.Anthropic())
```

### CrewAI
```python
from opik.integrations.crewai import track_crewai
track_crewai(project_name="my-project", crew=crew)
```

## Entrypoint Functions

Mark exactly ONE function with `entrypoint=True`:

```python
@opik.track(entrypoint=True, project_name="my-agent")
def run_agent(question: str) -> str:
    """Run the agent with a user question.

    Args:
        question: The user's question to answer.
    """
```

This enables:
- Triggering the agent from the Opik UI via `opik connect`
- Schema discovery for the UI input form
- Trace replay from the UI

## Thread ID for Conversations

For multi-turn agents:

```python
@opik.track(entrypoint=True)
def handle_message(session_id: str, message: str) -> str:
    opik.update_current_trace(thread_id=session_id)
    return generate_response(session_id, message)
```

## Common Pitfalls

- **Missing flush**: Scripts need `opik.flush_tracker()` before exit
- **Double-wrapping**: Don't use both `track_openai()` and `@opik.track` on the same call
- **Hardcoded config**: Extract model/temperature/prompt into an `opik.AgentConfig` subclass
- **Missing docstring**: The entrypoint function needs a docstring with `Args:` for schema discovery
- **Multiple top-level traces**: Use distributed tracing headers across service boundaries
