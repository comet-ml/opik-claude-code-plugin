# Opik Integrations Reference

Comprehensive guide to Opik's 80+ integrations organized by category.

## Python Agent Frameworks

### AG2 (AutoGen v2)

```python
from opik.integrations.ag2 import track_ag2

track_ag2()

# Your AG2 agents are now traced
```

### Agno

```python
from opik.integrations.agno import track_agno

track_agno()

# Agno agent operations traced
```

### Autogen

```python
from opik.integrations.autogen import track_autogen

track_autogen()

# AutoGen multi-agent conversations traced
```

### CrewAI

```python
from opik.integrations.crewai import track_crewai

track_crewai()

# All crew operations traced
from crewai import Agent, Task, Crew

agent = Agent(role="Researcher", goal="Research topics")
crew = Crew(agents=[agent], tasks=[...])
result = crew.kickoff()
```

### DSPy

```python
from opik.integrations.dspy import track_dspy
import dspy

track_dspy()

# DSPy modules and optimizers traced
```

### Google ADK (Agent Development Kit)

```python
from opik.integrations.adk import OpikTracer, track_adk_agent_recursive

opik_tracer = OpikTracer()
agent = ...  # Your ADK agent
track_adk_agent_recursive(agent, opik_tracer)
```

### Haystack

```python
from opik.integrations.haystack import OpikConnector

pipeline = Pipeline()
pipeline.add_component("opik", OpikConnector("pipeline-name"))
# Add other components...
```

### Instructor

```python
from opik.integrations.instructor import track_instructor
import instructor

client = track_instructor(instructor.from_openai(OpenAI()))
```

### LangChain

```python
from opik.integrations.langchain import OpikTracer
from langchain_openai import ChatOpenAI

tracer = OpikTracer()
llm = ChatOpenAI()
response = llm.invoke("Hello!", config={"callbacks": [tracer]})
```

### LangGraph

```python
from opik.integrations.langchain import OpikTracer

graph = ...  # Your LangGraph
app = graph.compile()

tracer = OpikTracer(graph=app.get_graph(xray=True))
result = app.invoke(
    {"messages": [HumanMessage(content="Hello")]},
    config={"callbacks": [tracer]}
)
```

### LiveKit Agents

```python
from opik.integrations.livekit import track_livekit

track_livekit()

# LiveKit agent interactions traced
```

### LlamaIndex

```python
from opik.integrations.llama_index import LlamaIndexCallbackHandler
from llama_index.core import Settings

Settings.callback_manager.add_handler(LlamaIndexCallbackHandler())
# All LlamaIndex operations traced
```

### Microsoft Agent Framework

```python
from opik.integrations.microsoft_agent import track_microsoft_agent

track_microsoft_agent()

# Microsoft Agent Framework operations traced
```

### OpenAI Agents (Swarm)

```python
from opik.integrations.openai_agents import track_openai_agents

track_openai_agents()

# OpenAI Agents/Swarm operations traced
```

### Pipecat

```python
from opik.integrations.pipecat import track_pipecat

track_pipecat()

# Pipecat pipeline operations traced
```

### Pydantic AI

```python
from opik.integrations.pydantic_ai import track_pydantic_ai
from pydantic_ai import Agent

track_pydantic_ai()

agent = Agent("openai:gpt-4")
# Agent runs traced
```

### Semantic Kernel

```python
from opik.integrations.semantic_kernel import track_semantic_kernel

track_semantic_kernel()

# Semantic Kernel operations traced
```

### Smolagents

```python
from opik.integrations.smolagents import track_smolagents

track_smolagents()

# Smolagents operations traced
```

### Strands Agents

```python
from opik.integrations.strands import track_strands

track_strands()

# Strands agent operations traced
```

### VoltAgent

```python
from opik.integrations.voltagent import track_voltagent

track_voltagent()

# VoltAgent operations traced
```

## TypeScript Frameworks

### BeeAI

```typescript
import { Opik } from "opik";
import { BeeAgent } from "bee-agent-framework";

const opik = new Opik();
// Configure BeeAI with Opik callbacks
```

### LangChain.js

```typescript
import { Opik } from "opik";
import { ChatOpenAI } from "@langchain/openai";

const opik = new Opik();
const tracer = opik.getLangChainTracer();

const llm = new ChatOpenAI();
await llm.invoke("Hello", { callbacks: [tracer] });
```

### Mastra

```typescript
import { Opik } from "opik";
import { Mastra } from "mastra";

const opik = new Opik();
// Configure Mastra with Opik
```

### Vercel AI SDK

```typescript
import { Opik } from "opik";
import { generateText } from "ai";
import { openai } from "@ai-sdk/openai";

const opik = new Opik();

const { text } = await generateText({
  model: openai("gpt-4"),
  prompt: "Hello",
  experimental_telemetry: {
    isEnabled: true,
    functionId: "my-function"
  }
});
```

## Model Providers

### OpenAI

```python
from opik.integrations.openai import track_openai
from openai import OpenAI

client = track_openai(OpenAI())

response = client.chat.completions.create(
    model="gpt-4",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### Anthropic

```python
from opik.integrations.anthropic import track_anthropic
import anthropic

client = track_anthropic(anthropic.Anthropic())

response = client.messages.create(
    model="claude-3-sonnet-20240229",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### AWS Bedrock

```python
from opik.integrations.bedrock import track_bedrock
import boto3

bedrock = boto3.client("bedrock-runtime")
tracked_client = track_bedrock(bedrock)

response = tracked_client.invoke_model(
    modelId="anthropic.claude-3-sonnet-20240229-v1:0",
    body=json.dumps({"prompt": "Hello"})
)
```

### BytePlus

```python
from opik.integrations.byteplus import track_byteplus

client = track_byteplus(BytePlusClient())
```

### Cohere

```python
from opik.integrations.cohere import track_cohere
import cohere

client = track_cohere(cohere.Client())

response = client.generate(prompt="Hello")
```

### DeepSeek

```python
from opik.integrations.deepseek import track_deepseek

client = track_deepseek(DeepSeekClient())
```

### Fireworks AI

```python
from opik.integrations.fireworks import track_fireworks

client = track_fireworks(FireworksClient())
```

### Google Gemini

```python
from opik.integrations.gemini import track_gemini
import google.generativeai as genai

track_gemini(genai)

model = genai.GenerativeModel("gemini-pro")
response = model.generate_content("Hello")
```

### Groq

```python
from opik.integrations.groq import track_groq
from groq import Groq

client = track_groq(Groq())

response = client.chat.completions.create(
    model="llama-3.1-70b-versatile",
    messages=[{"role": "user", "content": "Hello"}]
)
```

### Mistral AI

```python
from opik.integrations.mistral import track_mistral
from mistralai.client import MistralClient

client = track_mistral(MistralClient())
```

### Novita AI

```python
from opik.integrations.novita import track_novita

client = track_novita(NovitaClient())
```

### Ollama

```python
from opik.integrations.ollama import track_ollama
import ollama

track_ollama(ollama)

response = ollama.chat(
    model="llama3",
    messages=[{"role": "user", "content": "Hello"}]
)
```

### Together AI

```python
from opik.integrations.together import track_together
import together

client = track_together(together.Together())
```

### IBM WatsonX

```python
from opik.integrations.watsonx import track_watsonx

client = track_watsonx(WatsonXClient())
```

### xAI Grok

```python
from opik.integrations.xai import track_xai

client = track_xai(XAIClient())
```

## LLM Gateways

### Opik LLM Gateway

Native gateway for unified LLM access with built-in tracing.

```python
from opik.gateway import OpikGateway

gateway = OpikGateway()

# Route to any provider with automatic tracing
response = gateway.chat(
    provider="openai",
    model="gpt-4",
    messages=[{"role": "user", "content": "Hello"}]
)
```

### Kong AI Gateway

```python
# Configure Kong to forward Opik headers
# In Kong configuration:
# plugins:
#   - name: opik-tracing
#     config:
#       api_key: ${OPIK_API_KEY}
```

### LiteLLM

```python
from opik.integrations.litellm import track_litellm
import litellm

track_litellm()

# All LiteLLM calls traced regardless of provider
response = litellm.completion(
    model="gpt-4",  # or claude-3, gemini-pro, etc.
    messages=[{"role": "user", "content": "Hello"}]
)
```

### OpenRouter

```python
from opik.integrations.openrouter import track_openrouter

client = track_openrouter(OpenRouterClient())

# Access any model through OpenRouter with tracing
response = client.chat(
    model="anthropic/claude-3-opus",
    messages=[{"role": "user", "content": "Hello"}]
)
```

### Portkey

```python
from opik.integrations.portkey import track_portkey
from portkey_ai import Portkey

client = track_portkey(Portkey())
```

## No-Code Platforms

### Cursor

Enable Opik in Cursor settings:
1. Open Cursor Settings
2. Navigate to AI settings
3. Add Opik API key
4. Enable trace logging

### Dify

1. Go to Dify workspace settings
2. Add Opik as an observability provider
3. Configure API key and project name
4. All Dify workflows are automatically traced

### Flowise

1. Open Flowise admin panel
2. Go to Integrations > Observability
3. Add Opik configuration
4. Enable tracing for selected flows

### Langflow

1. Access Langflow settings
2. Configure Opik integration
3. Add API credentials
4. Flows automatically send traces

### n8n

Use the Opik node in n8n:
1. Add "Opik" node to workflow
2. Configure credentials
3. Connect to LLM nodes for automatic tracing

### OpenWebUI

1. Go to OpenWebUI admin settings
2. Navigate to Integrations
3. Enable Opik tracing
4. Configure API key

## OpenTelemetry

### Python (OpenTelemetry)

```python
from opentelemetry import trace
from opik.integrations.opentelemetry import OpikSpanExporter

# Configure OpenTelemetry with Opik exporter
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

provider = TracerProvider()
processor = BatchSpanProcessor(OpikSpanExporter())
provider.add_span_processor(processor)
trace.set_tracer_provider(provider)

# Your OpenTelemetry instrumented code
tracer = trace.get_tracer(__name__)
with tracer.start_as_current_span("my-operation"):
    # Operations are exported to Opik
    pass
```

### Ruby (OpenTelemetry)

```ruby
require 'opentelemetry/sdk'
require 'opik/opentelemetry'

OpenTelemetry::SDK.configure do |c|
  c.add_span_processor(
    OpenTelemetry::SDK::Trace::Export::BatchSpanProcessor.new(
      Opik::OpenTelemetry::Exporter.new
    )
  )
end
```

### Java (OpenTelemetry)

```java
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import com.opik.opentelemetry.OpikSpanExporter;

SdkTracerProvider tracerProvider = SdkTracerProvider.builder()
    .addSpanProcessor(
        BatchSpanProcessor.builder(new OpikSpanExporter()).build()
    )
    .build();
```

## Integration Best Practices

### Choosing an Integration

| Scenario | Recommended Integration |
|----------|------------------------|
| Single LLM provider | Provider-specific (OpenAI, Anthropic, etc.) |
| Multiple providers | LiteLLM or OpenRouter |
| Agent framework | Framework-specific (LangChain, CrewAI, etc.) |
| Existing OpenTelemetry | OpenTelemetry exporter |
| No-code platform | Platform-specific integration |

### Layering Integrations

You can combine integrations:

```python
from opik.integrations.openai import track_openai
from opik.integrations.langchain import OpikTracer

# Track both raw OpenAI calls and LangChain operations
openai_client = track_openai(OpenAI())
langchain_tracer = OpikTracer()

# LangChain with traced OpenAI underneath
llm = ChatOpenAI(client=openai_client)
response = llm.invoke("Hello", config={"callbacks": [langchain_tracer]})
```

### Environment Variables

All integrations respect these environment variables:

```bash
export OPIK_API_KEY="your-api-key"
export OPIK_URL_OVERRIDE="https://www.comet.com/opik/api"
export OPIK_PROJECT_NAME="my-project"
export OPIK_WORKSPACE="my-workspace"
export OPIK_TRACK_DISABLE="false"  # Set to "true" to disable
```
