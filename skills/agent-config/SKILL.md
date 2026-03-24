---
name: agent-config
description: Deep guide on Opik Agent Configuration — Blueprints (immutable config snapshots), environment tags (DEV/PROD), MaskIDs (A/B testing overlays), and the config lifecycle from development to production.
---

# Agent Configuration (Blueprints)

Opik 2.0's Agent Configuration system externalizes your agent's tunable parameters into managed, version-controlled configs.

## Core Concepts

| Concept | What It Is |
|---------|-----------|
| **Config Dataclass** | A Python dataclass with your agent's tunable parameters |
| **Blueprint** | An immutable snapshot of a config version |
| **Environment Tag** | A label (DEV, STAGING, PROD) pointing to a specific Blueprint |
| **MaskID** | A temporary override for A/B testing without creating new Blueprints |

## Creating a Config

Subclass `opik.AgentConfig` with typed fields. Use `Annotated` for descriptions.

```python
from typing import Annotated
import opik

class AgentConfig(opik.AgentConfig):
    model: Annotated[str, "LLM model to use"]
    temperature: Annotated[float, "Sampling temperature"]
    system_prompt: Annotated[str, "System prompt for the agent"]
    max_tokens: Annotated[int, "Maximum tokens in response"]
    top_p: Annotated[float, "Nucleus sampling parameter"]
```

**Rules:**
- Subclass `opik.AgentConfig` (NOT `@dataclass`)
- No default values on the class — pass values at instantiation
- All fields need type annotations

## Using Config in Your Agent

```python
config = AgentConfig(
    model="gpt-4o",
    temperature=0.7,
    system_prompt="You are a helpful assistant.",
    max_tokens=1024,
    top_p=1.0,
)

@opik.track(entrypoint=True, project_name="my-agent")
def run_agent(question: str) -> str:
    response = client.chat.completions.create(
        model=config.model,
        temperature=config.temperature,
        max_tokens=config.max_tokens,
        messages=[
            {"role": "system", "content": config.system_prompt},
            {"role": "user", "content": question},
        ],
    )
    return response.choices[0].message.content

# Publish to Opik for Blueprint management
opik_client = opik.Opik()
opik_client.create_agent_config_version(config, project_name="my-agent")
```

## Blueprint Lifecycle

```
Edit config → New Blueprint created (immutable)
          → DEV tag moves to new Blueprint
          → Test with Evaluation Suite
          → PASS? → Move PROD tag to new Blueprint
          → FAIL? → Keep PROD on previous Blueprint
```

## Environment Tags

| Tag | Purpose |
|-----|---------|
| `DEV` | Active development, latest changes |
| `STAGING` | Pre-production testing |
| `PROD` | Production — what end users see |

## MaskID Overlays

For A/B testing config variations without permanent changes:

```
Base Blueprint (PROD): temperature=0.7
├── MaskID-001: temperature=0.5
├── MaskID-002: temperature=0.9
└── MaskID-003: model="gpt-4o-mini"
```

Each MaskID is evaluated against the Evaluation Suite. The winning config gets promoted to a new Blueprint.

## What to Extract vs Not

### Extract (put in config)
- Model name, temperature, top_p, max_tokens
- System prompt / persona
- Any tunable parameter that affects agent behavior

### Don't Extract (keep in code/env)
- API keys and secrets → env vars
- Structural logic → code
- Truly constant values that never change

## Retrieving Config from Opik Backend

Inside a `@opik.track` decorated function:

```python
@opik.track(project_name="my-agent")
def run_agent(question: str):
    cfg = client.get_agent_config(
        fallback=AgentConfig(model="gpt-4o", temperature=0.7, ...),
        project_name="my-agent",
        latest=True,       # OR version="v1" OR env="prod"
    )
    # Access fields — triggers backend resolution
    response = llm_call(model=cfg.model, temperature=cfg.temperature)
```

## Deploying to Environments

```python
@opik.track(project_name="my-agent")
def deploy():
    cfg = client.get_agent_config(
        fallback=AgentConfig(...),
        project_name="my-agent",
        version="v2",
    )
    cfg.deploy_to("prod")  # Tag v2 as production
```

## Blueprint in Traces

Every trace includes `blueprint_id` metadata:
- Filter traces by Blueprint to compare config versions
- Roll back PROD tag if a Blueprint causes regression
- Track which config version produced each trace
