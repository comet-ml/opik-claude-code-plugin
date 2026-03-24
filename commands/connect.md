---
description: Connect your agent to Opik for triggering from the browser UI via the Local Runner
argument-hint: [--pair CODE]
allowed-tools:
  - Bash
  - Read
  - Grep
  - Glob
model: haiku
---

# Connect Agent to Opik (Local Runner)

Set up `opik connect` so the user's agent can be triggered from the Opik browser UI while running locally.

**User request:** $ARGUMENTS

## Step 1: Check Prerequisites

### 1a. Verify opik CLI is installed

Run `opik --version`. If not found:
- Check if `opik` is installed: `pip show opik` or `pip3 show opik`
- If not installed: `pip install opik`
- If installed but not on PATH: suggest `python -m opik --version`

### 1b. Verify there's an entrypoint function

Search the codebase for `entrypoint=True`:

```
grep -r "entrypoint=True" --include="*.py" .
```

If no entrypoint found:
- Tell the user: "No entrypoint function found. Run `/opik:instrument` first to add `entrypoint=True` to your main agent function."
- Stop here.

### 1c. Verify the entrypoint has a docstring with Args

Read the entrypoint function and check it has a docstring with `Args:` descriptions. The Local Runner uses this to build the input form in the UI. If missing, add it.

## Step 2: Detect Cloud vs OSS

Check for Opik configuration:

1. Check `OPIK_API_KEY` env var
2. Check `~/.opik.config` for `api_key` field
3. Check `OPIK_BASE_URL` or `url_override` in config

**If API key exists** → Cloud mode
**If no API key but URL points to localhost** → OSS mode
**If neither** → Run `opik configure` first

## Step 3: Connect

### Cloud Mode

```bash
opik connect
```

This automatically authenticates using the API key and registers the agent.

### OSS Mode

1. Tell the user: "Open the Opik UI in your browser and look for the 'Connect Agent' button to get a pairing code."
2. Once they provide the code:

```bash
opik connect --pair <CODE>
```

## Step 4: Verify Connection

After connecting:
- Confirm the runner is connected and listening
- Tell the user they can now go to the Opik UI and trigger their agent from the browser
- The agent will execute locally on their machine, and traces will appear in Opik

## Error Handling

| Error | Solution |
|-------|----------|
| "No entrypoint found" | Run `/opik:instrument` first |
| "Connection refused" | Check if Opik server is running (OSS) or API key is valid (Cloud) |
| "Invalid pair code" | Code expires — get a new one from the UI |
| "Port already in use" | Another runner may be active — check with `lsof -i :<port>` |
| "Authentication failed" | Run `opik configure` to set up credentials |

## Notes

- The runner stays active as long as the terminal is open
- Multiple agents can be connected simultaneously
- Traces from UI-triggered runs appear in the same project as local runs
- Config changes made in the UI take effect on the next run (via Blueprints)
