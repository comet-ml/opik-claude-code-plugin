# Opik Claude Code Plugin

Log Claude Code sessions to [Opik](https://github.com/comet-ml/opik) for LLM observability.

## How it works

```
User submits prompt  →  UserPromptSubmit hook  →  CREATE TRACE
        ↓
Claude uses tools    →  PostToolUse hooks      →  CREATE SPANS
        ↓
Claude finishes      →  Stop hook              →  END TRACE + CLEANUP
```

Each Q&A turn becomes an Opik trace. Each tool call becomes a span within that trace.
Multiple concurrent Claude Code sessions are isolated by session ID.

## Directory Structure

```
opik-claude-plugin/
├── .claude-plugin/
│   └── plugin.json       # Plugin metadata
├── hooks/
│   └── hooks.json        # Hook configuration (auto-loaded by Claude Code v2.1+)
├── scripts/
│   └── hooks/
│       └── opik-logger.sh  # Main logging script
└── README.md
```

## Requirements

- Claude Code CLI v2.1.0+
- `jq` - JSON processor
- `curl` - HTTP client
- Opik server running (local or cloud)

## Installation

### As a Plugin (recommended)

```bash
# From plugin marketplace (when available)
/plugin install opik-claude-code

# Or from GitHub
/plugin install github:comet-ml/opik-claude-plugin
```

### Manual Installation

1. Clone the repository:
   ```bash
   git clone https://github.com/comet-ml/opik-claude-plugin ~/.claude/plugins/opik-claude-plugin
   ```

2. The hooks will be auto-loaded by Claude Code v2.1+

## Configuration

Set environment variables for your Opik instance:

### Local Opik (default)

```bash
export OPIK_BASE_URL="http://localhost:5173/api/v1/private"
export OPIK_PROJECT="claude-code"
```

### Opik Cloud

```bash
export OPIK_BASE_URL="https://www.comet.com/opik/api/v1/private"
export OPIK_API_KEY="your-api-key"
export OPIK_WORKSPACE="your-workspace"
export OPIK_PROJECT="claude-code"
```

## State Management

Trace state is stored in `/tmp/opik-claude-{session_id}.json` during a turn.
The `Stop` hook cleans up this file after each turn completes.
Each session has its own temp file, ensuring isolation between concurrent sessions.

## License

MIT
