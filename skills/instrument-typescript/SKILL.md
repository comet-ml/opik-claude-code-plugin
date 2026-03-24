---
name: instrument-typescript
description: Step-by-step guide for adding Opik observability to TypeScript/JavaScript LLM applications. Covers the Opik client, framework integrations, and tracing patterns.
---

# Instrument TypeScript Agents with Opik

Guide to making your TypeScript/JavaScript agent observable with Opik.

## Quick Start

```typescript
import { Opik } from "opik";

const client = new Opik({
  projectName: process.env.OPIK_PROJECT_NAME || "my-agent",
});

async function agent(query: string): Promise<string> {
  const trace = client.trace({
    name: "my-agent",
    input: { query },
  });

  const searchSpan = trace.span({ name: "search", type: "tool" });
  const context = await searchDB(query);
  searchSpan.end({ output: { context } });

  const llmSpan = trace.span({ name: "generate", type: "llm" });
  const response = await llmCall(query, context);
  llmSpan.end({ output: { response } });

  trace.end({ output: { response } });
  await client.flush();
  return response;
}
```

## Framework Integrations

### OpenAI
```typescript
import { Opik } from "opik";
import { trackOpenAI } from "opik/openai";
import OpenAI from "openai";

const client = new Opik();
const openai = trackOpenAI(new OpenAI(), { client });
```

### Vercel AI SDK
```typescript
import { Opik } from "opik";
import { OpikTracer } from "opik/vercel";

const client = new Opik();
const tracer = new OpikTracer({ client });
```

## Thread ID for Conversations

```typescript
const trace = client.trace({
  name: "chat-turn",
  threadId: sessionId,  // Groups turns into a thread
  input: { message },
});
```

## Common Pitfalls

- **Missing flush**: Always `await client.flush()` before process exit
- **Project name**: Set via `OPIK_PROJECT_NAME` env var, not hardcoded
- **Span types**: Only `general`, `llm`, `tool`, `guardrail` are valid
