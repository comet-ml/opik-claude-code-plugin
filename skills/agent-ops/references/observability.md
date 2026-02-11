# LLM Observability Core Concepts

This document covers the foundational concepts behind Opik's tracing and observability platform.

## Why Observability Matters

LLM applications are complex systems with multiple moving parts:
- Pre-processing and prompt engineering
- Multiple LLM calls with different models
- Tool calling and function execution
- Retrieval-augmented generation (RAG)
- Post-processing and validation

Without observability, you're operating in the dark. You can't:
- Debug why an agent made a wrong decision
- Understand which step is causing latency
- Track costs across different operations
- Identify patterns in failures
- Optimize prompts with evidence

## Traces: The Complete Picture

A **trace** represents a complete execution path for a single interaction. Think of it as the "transaction" in your LLM application.

### Trace Anatomy

Every trace contains:

| Field | Description |
|-------|-------------|
| `id` | Unique identifier (UUID) |
| `name` | Descriptive name for the operation |
| `input` | The input data/prompt |
| `output` | The final output/response |
| `start_time` | When execution began |
| `end_time` | When execution completed |
| `metadata` | Custom key-value pairs |
| `tags` | Categorical labels |
| `feedback_scores` | Quality scores (manual or automated) |
| `thread_id` | Groups related traces |

### Trace Boundaries

Define clear trace boundaries that align with business operations:

**Good trace boundaries:**
- One user message → one agent response
- One API request → one API response
- One workflow execution

**Avoid:**
- Traces that span multiple user turns (use threads instead)
- Traces that include unrelated operations
- Overly granular traces (use spans for details)

## Spans: The Building Blocks

**Spans** are the individual operations within a trace. They form a hierarchical tree structure showing the call flow.

### Span Types

| Type | Description | Example |
|------|-------------|---------|
| `general` | Default for custom operations | Data processing |
| `llm` | LLM API calls | OpenAI completion |
| `tool` | Tool/function execution | Web search, calculator |
| `retrieval` | Data retrieval operations | Vector DB query |
| `guardrail` | Safety/validation checks | PII detection |

### Span Hierarchy

Spans can be nested to show parent-child relationships:

```
Trace: "Research Agent"
├── Span: "Plan Research" (general)
│   └── Span: "LLM Planning Call" (llm)
├── Span: "Execute Search" (tool)
│   ├── Span: "Web Search 1" (tool)
│   └── Span: "Web Search 2" (tool)
├── Span: "Synthesize Results" (general)
│   ├── Span: "Embed Documents" (retrieval)
│   └── Span: "Generate Summary" (llm)
└── Span: "Safety Check" (guardrail)
```

### Span Best Practices

1. **Use descriptive names**: `"Generate Product Description"` not `"llm_call_1"`
2. **Set appropriate types**: Enables filtering and specialized analytics
3. **Include relevant metadata**: Model name, temperature, token counts
4. **Time your spans properly**: Call `span.end()` when the operation completes

## Threads: Conversation Context

**Threads** group related traces into a coherent sequence, essential for:
- Multi-turn chat applications
- Complex workflows spanning multiple interactions
- User session tracking

### Thread Management

Create threads by setting a consistent `thread_id` across traces:

```python
import opik

@opik.track
def chat_turn(user_message: str, thread_id: str):
    opik.opik_context.update_current_trace(thread_id=thread_id)
    # Process message
    return response

# Same thread_id groups all turns together
chat_turn("Hello", thread_id="session-123")
chat_turn("What's the weather?", thread_id="session-123")
chat_turn("Thanks!", thread_id="session-123")
```

### Thread ID Strategies

Choose a thread ID strategy based on your use case:

| Strategy | Example | Use Case |
|----------|---------|----------|
| Session-based | `session-{user_id}-{session_id}` | Web applications with session tracking |
| UUID | `uuid.uuid4()` | Stateless APIs, each conversation unique |
| Timestamp | `{user_id}-{datetime}` | Time-ordered analysis, debugging |
| Semantic | `support-ticket-{ticket_id}` | Business process correlation |

```python
import uuid
from datetime import datetime

# Session-based
thread_id = f"session-{user_id}-{session_id}"

# UUID-based
thread_id = str(uuid.uuid4())

# Timestamp-based
thread_id = f"{user_id}-{datetime.now().isoformat()}"
```

### Thread vs Trace

| Concept | Scope | Use Case |
|---------|-------|----------|
| Trace | Single interaction | One request-response cycle |
| Thread | Multiple interactions | Complete conversation/workflow |

## Metadata Enrichment

Add context to your traces and spans for better analysis:

### Common Metadata Fields

```python
import opik

@opik.track
def my_function(input_text: str):
    opik.opik_context.update_current_trace(
        metadata={
            "user_id": "user-456",
            "environment": "production",
            "model_version": "v2.1",
            "experiment_id": "exp-789"
        },
        tags=["production", "gpt-4", "customer-support"]
    )
```

### Metadata Use Cases

- **A/B Testing**: Tag traces with experiment variants
- **User Segmentation**: Track behavior by user cohorts
- **Model Comparison**: Compare outputs across models
- **Cost Attribution**: Allocate costs to teams/projects
- **Debugging**: Filter to specific scenarios

## Feedback Scores

Attach quality assessments to traces for monitoring and evaluation:

### Score Types

| Type | Source | Example |
|------|--------|---------|
| Manual | Human review | Thumbs up/down |
| Automated | LLM-as-Judge | Hallucination score |
| Heuristic | Rule-based | Response length |
| User | End-user feedback | Satisfaction rating |

### Logging Feedback

```python
opik_context.update_current_trace(
    feedback_scores=[
        {"name": "relevance", "value": 0.95, "reason": "Answer directly addressed the question"},
        {"name": "hallucination", "value": 0.1, "reason": "Minor unsupported claim in paragraph 2"}
    ]
)
```

## Cost Tracking

Opik automatically tracks token usage and estimates costs:

### Tracked Metrics

- Input tokens
- Output tokens
- Total tokens
- Estimated cost (based on model pricing)

### Cost Analysis

Use the dashboard to:
- View cost per trace/project
- Identify expensive operations
- Track cost trends over time
- Set cost alerts

## Best Practices Summary

1. **Define clear trace boundaries** aligned with business operations
2. **Use meaningful span names** that describe the operation
3. **Set appropriate span types** for proper categorization
4. **Leverage threads** for multi-turn conversations
5. **Add relevant metadata** for filtering and analysis
6. **Attach feedback scores** to track quality
7. **Monitor costs** to prevent budget overruns
8. **Protect sensitive data** with appropriate filtering

## Distributed Tracing

For microservices and multi-service architectures, Opik supports distributed tracing to link traces across service boundaries.

### Getting Trace Headers

Extract headers from the current trace context to propagate to downstream services:

```python
import opik
import requests

@opik.track
def call_downstream_service(data):
    # Get headers for trace propagation
    headers = opik.opik_context.get_distributed_trace_headers()

    # Pass headers to downstream service
    response = requests.post(
        "https://downstream-service/api",
        json=data,
        headers=headers
    )
    return response.json()
```

### Using the Distributed Headers Context Manager

For cleaner code, use the `distributed_headers()` context manager:

```python
import opik
import httpx

@opik.track
async def orchestrator():
    async with httpx.AsyncClient() as client:
        # Automatically includes trace headers
        with opik.opik_context.distributed_headers() as headers:
            response = await client.post(
                "https://service-a/process",
                headers=headers,
                json={"task": "analyze"}
            )
    return response.json()
```

### Receiving Distributed Headers

In downstream services, accept and use the propagated headers:

```python
import opik
from flask import Flask, request

app = Flask(__name__)

@app.route("/api", methods=["POST"])
def handle_request():
    # Extract Opik headers from incoming request
    trace_headers = {
        k: v for k, v in request.headers.items()
        if k.startswith("opik-")
    }

    # Start a trace linked to the parent
    with opik.start_as_current_trace(
        "downstream-operation",
        distributed_headers=trace_headers
    ) as trace:
        result = process_request(request.json)
        trace.output = {"result": result}

    return {"result": result}
```

## Multimodal Tracing

Opik supports tracing multimodal AI applications that process images, videos, audio, and documents.

### Supported Media Types

| Type | Formats | Use Case |
|------|---------|----------|
| Images | PNG, JPEG, GIF, WebP | Vision models, image analysis |
| Video | MP4, WebM | Video understanding, frame extraction |
| Audio | MP3, WAV, M4A | Speech-to-text, audio analysis |
| Documents | PDF | Document QA, extraction |

### Attaching Images

```python
import opik
import base64

@opik.track
def analyze_image(image_path: str) -> str:
    # Read and encode image
    with open(image_path, "rb") as f:
        image_data = base64.b64encode(f.read()).decode()

    # Attach to current span
    opik.opik_context.update_current_span(
        metadata={
            "image": {
                "type": "image",
                "data": image_data,
                "media_type": "image/png"
            }
        }
    )

    # Process with vision model
    result = vision_model.analyze(image_path)
    return result
```

### Attaching from URL

```python
@opik.track
def analyze_remote_image(image_url: str):
    opik.opik_context.update_current_span(
        metadata={
            "image": {
                "type": "image",
                "url": image_url
            }
        }
    )
    return vision_model.analyze_url(image_url)
```

### Multimodal Trace Viewing

In the Opik UI:
- Images display inline in trace details
- Videos and audio show with playback controls
- PDFs render with page navigation
- All media types are searchable and filterable

## Data Privacy

Be mindful of sensitive data in traces:

- **PII**: Use Opik's anonymizers to mask personal information
- **Credentials**: Never log API keys or passwords
- **Business data**: Consider what should be retained
- **Compliance**: Ensure traces meet regulatory requirements
