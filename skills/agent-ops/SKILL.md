---
name: agent-ops
description: This skill should be used when the user asks about LLM observability, tracing, evaluation, Opik setup, agent monitoring, span creation, metrics, or debugging agent behavior. Use for questions like "how do I trace my agent", "set up Opik", "evaluate my LLM", "add observability", "monitor my agent", "debug my agent", "track LLM calls".
---

# LLM Observability with Opik

Opik is an open-source platform for LLM observability, evaluation, and optimization. It helps you understand, debug, and improve your LLM applications through comprehensive tracing, automated evaluation, and prompt optimization.

## Why LLM Observability Matters

LLM applications are complex systems that do more than just call an LLM API—they involve retrieval, pre-processing, post-processing, tool calling, and multi-step reasoning. Without observability, debugging issues, optimizing performance, and ensuring reliability becomes nearly impossible.

Observability enables you to:
- **Debug issues** by examining exactly what happened during each interaction
- **Optimize performance** by identifying bottlenecks and slow operations
- **Track costs** by monitoring token usage across your application
- **Ensure quality** by reviewing outputs and detecting hallucinations or errors
- **Iterate faster** by understanding how changes affect behavior

## Core Concepts

### Traces
A **trace** represents a complete execution path for a single interaction with an LLM or agent. It captures:
- Unique identifier for tracking
- Input prompts and output responses
- Timing information (start, end, duration)
- Metadata (model used, temperature, custom tags)
- Token usage and cost estimates

### Spans
A **span** represents an individual operation within a trace. Spans create a hierarchical structure showing:
- LLM calls
- Tool/function invocations
- Data retrieval operations
- Custom processing steps

Example hierarchy:
```
Trace: "Customer Support Chat"
├── Span: "Parse User Intent"
├── Span: "Query Knowledge Base"
│   ├── Span: "Search Vector Database"
│   └── Span: "Rank Results"
├── Span: "Generate Response"
│   ├── Span: "LLM Call: GPT-4"
│   └── Span: "Post-process Response"
└── Span: "Log Interaction"
```

### Threads
A **thread** groups related traces that form a conversation or workflow. Use threads to:
- Track multi-turn conversations
- Maintain context across LLM calls
- Analyze conversation patterns
- Debug user sessions

### Multimodal Tracing
Opik supports tracing multimodal content including images, videos, audio files, and PDFs. Attach media to traces and spans for complete observability of vision and audio AI applications.

### Metrics
**Metrics** provide quantitative assessments of your LLM outputs (41 built-in metrics):
- **Heuristic metrics**: Text similarity and validation (BLEU, ROUGE, Levenshtein, etc.)
- **Conversation heuristic metrics**: Multi-turn conversation analysis
- **LLM-as-Judge metrics**: Semantic evaluation (hallucination, relevance, helpfulness)
- **Conversation LLM metrics**: Quality assessment for chat applications
- **Agent-specific metrics**: Task completion, tool correctness, trajectory accuracy

## Quick Start

### Installation

**Python:**
```bash
pip install opik
opik configure  # Interactive setup
```

**TypeScript:**
```bash
npm install opik
```

Set environment variables:
```bash
export OPIK_API_KEY="your-api-key"
export OPIK_URL_OVERRIDE="https://www.comet.com/opik/api"  # Cloud
# export OPIK_URL_OVERRIDE="http://localhost:5173/api"    # Self-hosted
export OPIK_PROJECT_NAME="my-project"
```

### Basic Tracing (Python)

Using the `@opik.track` decorator:
```python
import opik

@opik.track
def retrieve_context(query: str) -> list:
    # Your retrieval logic
    return ["context1", "context2"]

@opik.track
def generate_response(query: str, context: list) -> str:
    # Your LLM call
    return "Generated response"

@opik.track(name="my_agent")
def agent(query: str) -> str:
    context = retrieve_context(query)
    return generate_response(query, context)

# All nested calls are automatically traced
result = agent("What is machine learning?")
```

### Basic Tracing (TypeScript)

```typescript
import { Opik } from "opik";

const client = new Opik();

const trace = client.trace({
  name: "my-agent",
  input: { prompt: "Hello!" },
});

const span = trace.span({
  name: "llm-call",
  type: "llm",
  input: { prompt: "Hello!" },
});

// Your LLM call here
span.end({ output: { response: "Hi there!" } });
trace.end({ output: { response: "Hi there!" } });

await client.flush();
```

### Framework Integrations

Opik integrates with 80+ frameworks and providers:

**Python Frameworks:** AG2, Agno, Autogen, CrewAI, DSPy, Google ADK, Haystack, Instructor, LangChain, LangGraph, LiveKit Agents, LlamaIndex, Microsoft Agent Framework, OpenAI Agents, Pipecat, Pydantic AI, Semantic Kernel, Smolagents, Strands Agents, VoltAgent

**TypeScript Frameworks:** BeeAI, LangChain.js, Mastra, Vercel AI SDK

**Model Providers:** OpenAI, Anthropic, Bedrock, BytePlus, Cohere, DeepSeek, Fireworks AI, Gemini, Groq, Mistral, Novita AI, Ollama, Together AI, WatsonX, xAI Grok

**Gateways:** Opik LLM Gateway, Kong AI Gateway, LiteLLM, OpenRouter, Portkey

**No-Code:** Cursor, Dify, Flowise, Langflow, n8n, OpenWebUI

**OpenTelemetry:** Python, Ruby, Java

Example with OpenAI (Python):
```python
from opik.integrations.openai import track_openai
from openai import OpenAI

client = track_openai(OpenAI())
# All calls are now automatically traced
response = client.chat.completions.create(
    model="gpt-4",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

## Deployment Options

- **Opik Cloud**: Managed service at comet.com/opik
- **Self-hosted**: Deploy with Docker or Kubernetes
- **Open source**: Full access to the codebase on GitHub

## Next Steps

For detailed information, refer to the reference documentation in this skill:

| Topic | Reference File |
|-------|----------------|
| Tracing concepts & best practices | `references/observability.md` |
| Python SDK tracing | `references/tracing-python.md` |
| TypeScript SDK tracing | `references/tracing-typescript.md` |
| REST API tracing | `references/tracing-rest-api.md` |
| Evaluation & metrics | `references/evaluation.md` |
| Agent architecture patterns | `references/agent-patterns.md` |
| Prompt & agent optimization | `references/optimization.md` |
| Production monitoring | `references/production.md` |
| Integrations reference | `references/integrations.md` |
