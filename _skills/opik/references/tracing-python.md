# Python SDK Tracing Guide

Complete guide to tracing LLM applications with the Opik Python SDK.

## Installation & Configuration

```bash
pip install opik
opik configure  # Interactive setup
```

Or set environment variables:
```bash
export OPIK_API_KEY="your-api-key"
export OPIK_URL_OVERRIDE="https://www.comet.com/opik/api"  # Cloud
export OPIK_PROJECT_NAME="my-project"
```

## The @opik.track Decorator

The `@opik.track` decorator is the simplest way to add tracing:

```python
import opik

@opik.track
def my_function(input_text: str) -> str:
    # Your logic here
    return f"Processed: {input_text}"
```

### Decorator Options

```python
import opik

@opik.track(
    name="custom_operation_name",  # Override function name
    type="llm",                    # Span type: general, llm, tool, guardrail
    project_name="my-project",     # Override project
    tags=["production", "v2"],     # Add tags
    metadata={"version": "1.0"},   # Add metadata
    flush=True                     # Flush immediately (for scripts)
)
def my_function():
    pass
```

### Nested Functions

Decorated functions automatically create nested spans:

```python
import opik

@opik.track
def retrieve_context(query: str) -> list:
    return ["doc1", "doc2"]

@opik.track
def call_llm(prompt: str) -> str:
    return "LLM response"

@opik.track(name="rag_pipeline")
def rag_agent(query: str) -> str:
    context = retrieve_context(query)    # Creates child span
    prompt = f"Context: {context}\nQuery: {query}"
    response = call_llm(prompt)          # Creates child span
    return response

# Creates trace with two nested spans
result = rag_agent("What is ML?")
```

## Context Management

Use `opik.opik_context` to update trace/span data dynamically:

```python
import opik

@opik.track
def my_agent(query: str):
    # Update the current trace
    opik.opik_context.update_current_trace(
        thread_id="conversation-123",
        tags=["customer-support"],
        metadata={"user_id": "user-456"},
        feedback_scores=[
            {"name": "quality", "value": 0.9}
        ]
    )

    # Update the current span
    opik.opik_context.update_current_span(
        metadata={"model": "gpt-4", "temperature": 0.7}
    )

    return "Response"
```

### Getting Current Context

```python
import opik

# Get current trace data
trace_data = opik.opik_context.get_current_trace_data()
print(trace_data.id)  # Trace ID

# Get current span data
span_data = opik.opik_context.get_current_span_data()
print(span_data.id)  # Span ID
```

## Using opik_args

Pass trace/span configuration through function calls:

```python
import opik

@opik.track
def my_function(text: str) -> str:
    return f"Processed: {text}"

# Call with additional tracing configuration
result = my_function(
    "hello world",
    opik_args={
        "span": {
            "tags": ["important"],
            "metadata": {"priority": "high"}
        },
        "trace": {
            "thread_id": "session-123",
            "tags": ["production"]
        }
    }
)
```

## Context Managers

For more control, use context managers:

### Trace Context Manager

```python
# Signature:
# opik.start_as_current_trace(
#     name: str,
#     input: Optional[Dict] = None,
#     output: Optional[Dict] = None,
#     tags: Optional[List[str]] = None,
#     metadata: Optional[Dict] = None,
#     project_name: Optional[str] = None,
#     thread_id: Optional[str] = None,
#     flush: bool = False,
# ) -> Generator[TraceData]
#
# TraceData attributes (set via assignment):
#   .input, .output, .tags, .metadata, .thread_id

import opik

with opik.start_as_current_trace("my-trace", project_name="my-project") as trace:
    trace.input = {"query": "What is ML?"}

    # Your logic here

    trace.output = {"response": "Machine learning is..."}
    trace.tags = ["production"]
    trace.metadata = {"model": "gpt-4"}
```

### Span Context Manager

```python
# Signature:
# opik.start_as_current_span(
#     name: str,
#     type: Literal["general", "tool", "llm", "guardrail"] = "general",
#     input: Optional[Dict] = None,
#     output: Optional[Dict] = None,
#     tags: Optional[List[str]] = None,
#     metadata: Optional[Dict] = None,
#     project_name: Optional[str] = None,
#     model: Optional[str] = None,
#     provider: Optional[str] = None,
#     flush: bool = False,
# ) -> Generator[SpanData]
#
# SpanData attributes (set via assignment):
#   .input, .output, .tags, .metadata, .model, .provider, .usage

import opik

with opik.start_as_current_trace("my-trace") as trace:
    trace.input = {"query": "Hello"}

    with opik.start_as_current_span("llm-call", type="llm") as span:
        span.input = {"prompt": "Hello"}
        # Make LLM call
        span.output = {"response": "Hi there!"}
        span.model = "gpt-4"
        span.provider = "openai"
        span.usage = {
            "prompt_tokens": 5,
            "completion_tokens": 3,
            "total_tokens": 8
        }

    trace.output = {"response": "Hi there!"}
```

## Low-Level SDK

For maximum control, use the Opik client directly:

```python
from opik import Opik

client = Opik(project_name="my-project")

# Signature:
# client.trace(
#     id: Optional[str] = None,
#     name: Optional[str] = None,
#     start_time: Optional[datetime] = None,
#     end_time: Optional[datetime] = None,
#     input: Optional[Dict] = None,
#     output: Optional[Dict] = None,
#     metadata: Optional[Dict] = None,
#     tags: Optional[List[str]] = None,
#     feedback_scores: Optional[List[FeedbackScoreDict]] = None,
#     project_name: Optional[str] = None,
#     error_info: Optional[ErrorInfoDict] = None,
#     thread_id: Optional[str] = None,
# ) -> Trace

trace = client.trace(
    name="my_trace",
    input={"query": "Hello"},
    metadata={"user_id": "123"}
)

# Signature:
# trace.span(
#     id: Optional[str] = None,
#     parent_span_id: Optional[str] = None,
#     name: Optional[str] = None,
#     type: Literal["general", "tool", "llm", "guardrail"] = "general",
#     start_time: Optional[datetime] = None,
#     end_time: Optional[datetime] = None,
#     metadata: Optional[Dict] = None,
#     input: Optional[Dict] = None,
#     output: Optional[Dict] = None,
#     tags: Optional[List[str]] = None,
#     usage: Optional[Dict | OpikUsage] = None,
#     model: Optional[str] = None,
#     provider: Optional[str | LLMProvider] = None,
#     error_info: Optional[ErrorInfoDict] = None,
#     total_cost: Optional[float] = None,
# ) -> Span

span1 = trace.span(
    name="preprocessing",
    input={"text": "Hello"}
)
# Signature:
# span.end(
#     end_time: Optional[datetime] = None,
#     metadata: Optional[Dict] = None,
#     input: Optional[Dict] = None,
#     output: Optional[Dict] = None,
#     tags: Optional[List[str]] = None,
#     usage: Optional[Dict | OpikUsage] = None,
#     model: Optional[str] = None,
#     provider: Optional[str | LLMProvider] = None,
#     error_info: Optional[ErrorInfoDict] = None,
#     total_cost: Optional[float] = None,
# ) -> None
span1.end(output={"processed": "HELLO"})

span2 = trace.span(
    name="llm_call",
    type="llm",
    input={"prompt": "Respond to: HELLO"}
)
span2.end(output={"response": "Hi!"})

# Signature:
# trace.end(
#     end_time: Optional[datetime] = None,
#     metadata: Optional[Dict] = None,
#     input: Optional[Dict] = None,
#     output: Optional[Dict] = None,
#     tags: Optional[List[str]] = None,
#     error_info: Optional[ErrorInfoDict] = None,
#     thread_id: Optional[str] = None,
# ) -> None
trace.end(output={"response": "Hi!"})

# Ensure all data is sent
client.flush()
```

## Distributed Tracing

For microservices architectures, propagate trace context across service boundaries.

### Getting Headers for Downstream Calls

```python
import opik
import httpx

@opik.track
def call_microservice(data: dict):
    # Get trace headers for propagation
    headers = opik.opik_context.get_distributed_trace_headers()

    response = httpx.post(
        "https://service-b/api/process",
        json=data,
        headers=headers
    )
    return response.json()
```

### Using the Distributed Headers Context Manager

On the receiving side, use `distributed_headers` to link into an existing trace:

```python
from opik.decorator.context_manager import distributed_headers

# In the receiving service, use the context manager with headers from the request
with distributed_headers(incoming_headers):
    # Code here runs within the parent trace context
    result = process_request(data)
```

### Receiving Headers in Downstream Service

```python
import opik
from fastapi import FastAPI, Request

app = FastAPI()

@app.post("/api/process")
async def process(request: Request):
    # Extract Opik headers
    dist_headers = {
        k: v for k, v in request.headers.items()
        if k.lower().startswith("opik-")
    }

    # Link to parent trace
    with opik.start_as_current_trace(
        "process-request",
        distributed_headers=dist_headers
    ) as trace:
        result = await do_processing(await request.json())
        trace.output = result

    return result
```

## Multimodal Attachments

Attach images, audio, video, and documents to your traces.

### Image Attachments

```python
import opik
import base64

@opik.track
def analyze_image(image_path: str):
    with open(image_path, "rb") as f:
        image_b64 = base64.b64encode(f.read()).decode()

    opik.opik_context.update_current_span(
        metadata={
            "input_image": {
                "type": "image",
                "data": image_b64,
                "media_type": "image/png"
            }
        }
    )

    result = vision_model.analyze(image_path)
    return result
```

### URL-Based Attachments

```python
@opik.track
def process_media(url: str):
    opik.opik_context.update_current_span(
        metadata={
            "media": {
                "type": "image",  # or "video", "audio", "pdf"
                "url": url
            }
        }
    )
    return process(url)
```

### Audio Transcription Example

```python
@opik.track
def transcribe_audio(audio_path: str):
    with open(audio_path, "rb") as f:
        audio_b64 = base64.b64encode(f.read()).decode()

    opik.opik_context.update_current_span(
        metadata={
            "audio_file": {
                "type": "audio",
                "data": audio_b64,
                "media_type": "audio/mp3"
            }
        }
    )

    transcript = whisper.transcribe(audio_path)
    return transcript
```

## Framework Integrations

### OpenAI

```python
from opik.integrations.openai import track_openai
from openai import OpenAI

client = track_openai(OpenAI())

# All calls are automatically traced
response = client.chat.completions.create(
    model="gpt-4",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### LangChain

```python
from opik.integrations.langchain import OpikTracer
from langchain_openai import ChatOpenAI

tracer = OpikTracer()

llm = ChatOpenAI()
response = llm.invoke(
    "Hello!",
    config={"callbacks": [tracer]}
)
```

### LangGraph

```python
from opik.integrations.langchain import OpikTracer

# Create your graph
graph = ...
app = graph.compile()

# Create tracer with graph visualization
tracer = OpikTracer(graph=app.get_graph(xray=True))

result = app.invoke(
    {"messages": [HumanMessage(content="Hello")]},
    config={"callbacks": [tracer]}
)
```

### Anthropic

```python
from opik.integrations.anthropic import track_anthropic
import anthropic

client = track_anthropic(anthropic.Anthropic())

response = client.messages.create(
    model="claude-3-sonnet",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### ADK (Google Agent Development Kit)

```python
from opik.integrations.adk import OpikTracer, track_adk_agent_recursive

opik_tracer = OpikTracer()

# Define your ADK agent
agent = ...

# Wrap the agent
track_adk_agent_recursive(agent, opik_tracer)
```

### CrewAI

```python
from opik.integrations.crewai import track_crewai
from crewai import Agent, Task, Crew

agent = Agent(role="Researcher", goal="Research topics", backstory="Expert researcher")
task = Task(description="Research ML", agent=agent)
crew = Crew(agents=[agent], tasks=[task])

# crew= parameter is required for v1.0.0+
track_crewai(project_name="my-project", crew=crew)

result = crew.kickoff()
```

### LlamaIndex

```python
from opik.integrations.llama_index import LlamaIndexCallbackHandler
from llama_index.core import Settings

# Set up global callback
Settings.callback_manager.add_handler(
    LlamaIndexCallbackHandler()
)

# All LlamaIndex operations are now traced
```

### DSPy

```python
from opik.integrations.dspy import OpikCallback
import dspy

dspy.configure(callbacks=[OpikCallback()])

# DSPy modules and optimizers are now traced
```

### Pydantic AI

Pydantic AI uses Logfire for observability. Bridge to Opik via OTLP:

```python
import logfire

logfire.configure(send_to_logfire=False)
logfire.instrument_pydantic_ai()

from pydantic_ai import Agent
agent = Agent("openai:gpt-4")
# Agent runs are now traced via OTLP to Opik
```

### Bedrock

```python
from opik.integrations.bedrock import track_bedrock
import boto3

bedrock = boto3.client("bedrock-runtime")
tracked_client = track_bedrock(bedrock)

response = tracked_client.invoke_model(
    modelId="anthropic.claude-3-sonnet",
    body=json.dumps({"prompt": "Hello"})
)
```

### Groq

Groq exposes an OpenAI-compatible API. Use `track_openai` with Groq's base URL:

```python
from opik.integrations.openai import track_openai
from openai import OpenAI
import os

client = track_openai(OpenAI(
    base_url="https://api.groq.com/openai/v1",
    api_key=os.environ["GROQ_API_KEY"]
))

response = client.chat.completions.create(
    model="llama-3.1-70b-versatile",
    messages=[{"role": "user", "content": "Hello"}]
)
```

### Ollama

**Option 1: OpenAI-compatible mode** (recommended)

```python
from opik.integrations.openai import track_openai
from openai import OpenAI

client = track_openai(OpenAI(
    base_url="http://localhost:11434/v1",
    api_key="ollama"
))

response = client.chat.completions.create(
    model="llama3",
    messages=[{"role": "user", "content": "Hello"}]
)
```

**Option 2: Manual with @opik.track**

```python
import opik
import ollama

@opik.track(type="llm")
def chat_with_ollama(prompt: str) -> str:
    response = ollama.chat(
        model="llama3",
        messages=[{"role": "user", "content": prompt}]
    )
    opik.opik_context.update_current_span(
        model="llama3",
        provider="ollama"
    )
    return response["message"]["content"]
```

### LiteLLM

The Opik integration is on the LiteLLM side via callback:

```python
from litellm.integrations.opik.opik import OpikLogger
import litellm

litellm.callbacks = [OpikLogger()]

# All LiteLLM calls are traced regardless of provider
response = litellm.completion(
    model="gpt-4",
    messages=[{"role": "user", "content": "Hello"}]
)
```

## Async Support

The SDK fully supports async functions:

```python
import opik

@opik.track
async def async_agent(query: str) -> str:
    context = await retrieve_context(query)
    response = await call_llm(context, query)
    return response
```

## Flushing Data

The SDK batches data for performance. Flush manually for scripts:

```python
from opik import Opik

client = Opik()

# Your tracing code...

# Ensure all data is sent
client.flush()
```

For `@opik.track` decorator usage, use `flush_tracker()`:

```python
import opik

@opik.track
def my_pipeline():
    # Your traced operations
    pass

my_pipeline()
opik.flush_tracker()  # Flush data from @opik.track decorator usage
```

Or use `flush=True` on the decorator for automatic flush on completion:

```python
import opik

@opik.track(flush=True)
def my_script():
    # Short-lived script that needs immediate flush
    pass
```

## Disabling Tracing

Disable tracing without code changes:

```bash
export OPIK_TRACK_DISABLE=true
```

Or programmatically:

```python
import opik

opik.set_tracing_active(False)  # Disable
opik.set_tracing_active(True)   # Re-enable

print(opik.is_tracing_active())  # Check status
```

## Error Handling

Traces automatically capture errors:

```python
import opik

@opik.track
def risky_operation():
    try:
        # Operation that might fail
        result = dangerous_call()
    except Exception as e:
        opik.opik_context.update_current_trace(
            error_info={"error": str(e), "type": type(e).__name__}
        )
        raise

# Error info is captured in the trace
```

## Best Practices

1. **Use decorators for simplicity**: Start with `@opik.track` and add complexity as needed
2. **Name spans descriptively**: Use action-oriented names like `"Generate Summary"`
3. **Set span types correctly**: Enables specialized analytics
4. **Add metadata for debugging**: User IDs, versions, experiment flags
5. **Use threads for conversations**: Group related traces with `thread_id`
6. **Flush in scripts**: Call `client.flush()` before exit
7. **Avoid sensitive data**: Don't log PII or secrets in traces
