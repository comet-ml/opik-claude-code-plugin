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

## Configuration

Run the Opik CLI to configure your connection:

```bash
opik configure
```

This creates `~/.opik.config` with your API URL, key, and workspace.

### Optional Environment Variables

```bash
export OPIK_PROJECT="claude-code"           # Project name (default: claude-code)
export OPIK_CC_DEBUG="true"                 # Enable debug logging
export OPIK_CC_TRUNCATE_FIELDS="false"      # Don't truncate large fields
```

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

### `/opik` - Control Tracing

```bash
/opik start tracing           # Enable tracing for this project
/opik stop tracing            # Disable tracing for this project
/opik status                  # Check current tracing status

/opik start tracing --global  # Enable tracing for all projects
/opik stop tracing --global   # Disable tracing globally
```

Tracing state is stored in `.claude/.opik-tracing-enabled` (project) or `~/.claude/.opik-tracing-enabled` (global). Project settings take precedence.

**Note**: Changes affect new sessions only.

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
│   └── opik.md             # /opik command
└── mcp-configs/
    └── mcp-servers.json    # MCP server configurations
```

## Building from Source

```bash
make build        # Build for all platforms
make build-local  # Build for current platform only
```

## License

Apache-2.0
