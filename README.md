# Opik Claude Code Plugin

Log Claude Code sessions to [Opik](https://github.com/comet-ml/opik) for LLM observability, plus skills and agents for building observable AI applications.

## Features

- **Session Tracing**: Automatically log Claude Code sessions as Opik traces
- **Span Tracking**: Each tool call becomes a span within the trace
- **Subagent Support**: Nested agent calls are tracked with parent-child relationships
- **Skills**: Built-in knowledge for LLM observability, tracing, and evaluation
- **Agents**: Code review agent for agent architecture best practices

## How Tracing Works
We trigger tracing for everything done in Claude Code, but don't slow you down. 

```
User submits prompt  →  UserPromptSubmit hook  →  CREATE TRACE
        ↓
Claude uses tools    →  PostToolUse hooks      →  CREATE SPANS
        ↓
Claude finishes      →  Stop hook              →  END TRACE
```

Each conversation turn becomes an Opik trace. Tool calls, thoughts, and responses become spans. Subagent invocations are nested under their parent Task span.

## Installation

```bash
claude plugins add github:comet-ml/opik-claude-plugin
```

Or from within Claude Code:

```
/install github:comet-ml/opik-claude-plugin
```

**Important:** Restart any running Claude Code sessions after installation. Hooks only load when a session starts.

## Configuration

Run the Opik CLI to configure your connection:

```bash
opik configure
```

This creates `~/.opik.config` with your API URL, key, and workspace.

### Optional Environment Variables

```bash
export OPIK_CC_PROJECT="my-project"         # Project name (default: claude-code)
export OPIK_CC_TRUNCATE_FIELDS="false"      # Don't truncate large fields
```

All plugin env vars use the `OPIK_CC_` prefix to avoid conflicts with standard Opik SDK variables.

### External Trace Linking

Link Claude Code sessions to existing Opik traces (useful for embedding Claude Code in larger workflows):

```bash
export OPIK_CC_PARENT_TRACE_ID="your-trace-id"  # Attach to existing trace
export OPIK_CC_ROOT_SPAN_ID="your-span-id"      # Set parent span for all Claude Code spans
```

## MCP Server Setup

The [Opik MCP server](https://github.com/comet-ml/opik-mcp) provides Claude with tools to interact with your Opik data - query traces, analyze experiments, and access evaluation results directly in conversation.

### For Opik Cloud

Add to your `~/.claude.json`:

```json
{
  "mcpServers": {
    "opik": {
      "command": "npx",
      "args": ["-y", "opik-mcp", "--apiKey", "YOUR_OPIK_API_KEY"]
    }
  }
}
```

Replace `YOUR_OPIK_API_KEY` with your API key from [comet.com](https://www.comet.com).

### For Self-Hosted Opik

```json
{
  "mcpServers": {
    "opik": {
      "command": "npx",
      "args": ["-y", "opik-mcp", "--apiBaseUrl", "http://localhost:5173/api"]
    }
  }
}
```

Adjust the `apiBaseUrl` to match your Opik instance.

### Pre-configured Templates

Copy-ready configurations are available in `mcp-configs/mcp-servers.json`.

## Commands

### `/opik session` - Claude Code Session Tracing

Enable/disable automatic tracing of your Claude Code sessions to Opik.

```bash
/opik session start                 # Enable tracing for this project
/opik session start --debug         # Enable tracing + debug logging
/opik session stop                  # Disable tracing for this project
/opik session status                # Check current tracing status

/opik session start --global        # Enable tracing for all projects
/opik session stop --global         # Disable tracing globally
```

Tracing state is stored in `.claude/.opik-tracing-enabled` (project) or `~/.claude/.opik-tracing-enabled` (global). Project settings take precedence.

**Note:** Restart Claude Code sessions for changes to take effect.

### `/opik instrument` - Add Observability to Your Code

Automatically detect frameworks in your code and add the correct Opik integration.

```bash
/opik instrument my_agent.py        # Add tracing to a specific file
/opik instrument                    # Analyze current context and add tracing
```

Supports automatic detection and integration for:
- **Python:** OpenAI, Anthropic, LangChain, LlamaIndex, CrewAI, Bedrock, Groq, LiteLLM
- **TypeScript:** OpenAI, LangChain, Vercel AI SDK
- **Custom code:** Adds `@opik.track` decorators or `Opik` client usage

The command ensures tracing starts at your entry point (critical for replay capability) and uses the correct span types.

## Skills

### `/agent-ops` - LLM Observability Knowledge

Comprehensive guidance on:
- Opik setup and configuration
- Tracing with Python/TypeScript SDKs
- 80+ framework integrations (LangChain, CrewAI, OpenAI, Anthropic, etc.)
- Evaluation with 41 built-in metrics
- Production monitoring, guardrails, and debugging

## Agents

### `agent-reviewer`

Reviews agent code for:
- Idempotence and retry safety
- Security vulnerabilities
- Architecture patterns
- State management
- Observability hooks

## Directory Structure

```
opik-claude-plugin/
├── .claude-plugin/
│   ├── plugin.json         # Plugin manifest
│   └── marketplace.json    # Marketplace definition (source: "./")
├── hooks/
│   └── hooks.json          # Hook configuration
├── scripts/
│   └── opik-logger         # Platform selector script
├── bin/
│   └── opik-logger-*       # Compiled binaries (darwin/linux, amd64/arm64)
├── src/
│   └── *.go                # Go source code
├── skills/
│   └── agent-ops/          # Observability skill + references
├── agents/
│   └── agent-reviewer.md   # Agent review agent
├── commands/
│   ├── opik-session.md     # /opik session command
│   └── opik-instrument.md  # /opik instrument command
└── mcp-configs/
    └── mcp-servers.json    # MCP server configurations
```

## Building from Source

```bash
make build        # Build for all platforms
make build-local  # Build for current platform only
```
