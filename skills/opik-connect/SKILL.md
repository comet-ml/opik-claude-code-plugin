---
name: opik-connect
description: Guide for setting up Opik Connect (Local Runner) to pair your local agent with the Opik browser UI. Covers Cloud and OSS pairing, troubleshooting, and networking.
---

# Opik Connect (Local Runner)

Opik Connect lets you trigger your agent from the Opik browser UI while it runs on your local machine.

## Prerequisites

1. **opik CLI installed**: `pip install opik`
2. **Agent instrumented** with `entrypoint=True` on the main function
3. **Docstring with Args** on the entrypoint (for UI input form)

## Setup

### Cloud (API Key)

```bash
opik configure  # Set API key if not done
opik connect
```

### OSS (Self-Hosted)

```bash
# 1. Open Opik UI in browser
# 2. Click "Connect Agent" to get a pair code
# 3. Run:
opik connect --pair ABCDEF
```

## What Happens

1. Runner registers the entrypoint function with Opik
2. Opik UI shows the agent with an input form (derived from the docstring)
3. User types input → clicks Run → agent executes locally
4. Full trace appears in Opik with spans, token usage, cost

## Features

| Feature | Description |
|---------|-------------|
| UI triggering | Type input in browser, execute locally |
| Trace replay | Click "Re-run" on any trace |
| Config iteration | Edit config in UI → re-run → compare |
| Parallel jobs | Runner handles concurrent executions |

## Troubleshooting

| Issue | Solution |
|-------|----------|
| "No entrypoint found" | Add `entrypoint=True` to `@opik.track` on main function |
| "Connection refused" | Check Opik server is running (OSS) or API key is valid (Cloud) |
| "Invalid pair code" | Codes expire — get a new one from the UI |
| "Port in use" | Another runner may be active |
| Runner disconnects | Check network; runner auto-reconnects |
| Agent not showing in UI | Verify the entrypoint docstring has `Args:` |

## Networking (OSS)

- Runner connects outbound to the Opik server (no inbound ports needed)
- Default: connects to `http://localhost:5173`
- Override: set `OPIK_BASE_URL` env var
- Heartbeat keeps connection alive during idle periods
