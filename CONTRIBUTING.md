# Contributing to the Opik Claude Code Plugin

We're excited that you're interested in contributing! There are many ways to help:

* Submit [bug reports](https://github.com/comet-ml/opik-claude-code-plugin/issues) and [feature requests](https://github.com/comet-ml/opik-claude-code-plugin/issues)
* Improve the documentation
* Add new skills, commands, or agents
* Enhance the session tracing logger

## Local Development Setup

### Prerequisites

- [Claude Code](https://claude.ai/claude-code) CLI installed
- [Go 1.21+](https://go.dev/dl/) (only needed if modifying the tracing logger)

### Clone and Install Locally

```bash
git clone https://github.com/comet-ml/opik-claude-code-plugin.git
cd opik-claude-code-plugin
```

Install the plugin from your local clone:

```
/plugin marketplace add /path/to/opik-claude-code-plugin
/plugin install opik
```

Restart Claude Code after installing.

### Reinstalling After Changes

After making changes to the plugin source, reinstall to pick them up:

```
/plugin uninstall opik
/plugin install opik
```

Then restart Claude Code. The plugin cache is at `~/.claude/plugins/cache/opik/`.

## Project Structure

```
opik-claude-code-plugin/
├── .claude-plugin/
│   ├── plugin.json         # Plugin manifest
│   └── marketplace.json    # Marketplace definition
├── hooks/
│   └── hooks.json          # Hook configuration (session tracing)
├── scripts/
│   └── opik-logger         # Platform selector script
├── bin/
│   └── opik-logger-*       # Compiled binaries (darwin/linux, amd64/arm64)
├── src/
│   └── *.go                # Go source for the tracing logger
├── skills/
│   └── agent-ops/          # LLM observability skill + references
├── agents/
│   └── agent-reviewer.md   # Agent code review agent
├── commands/
│   ├── instrument.md       # /opik:instrument command
│   └── trace-claude-code.md  # /opik:trace-claude-code command
└── mcp-configs/
    └── mcp-servers.json    # MCP server configurations
```

## Contributing Skills

Skills live in `skills/` as directories containing a `SKILL.md` file and optional `references/` directory.

```
skills/
└── my-skill/
    ├── SKILL.md            # Main skill content
    └── references/         # Optional supporting docs
        └── details.md
```

The `SKILL.md` file is loaded when the skill is invoked. Reference files provide deeper knowledge that the skill can point to. Keep `SKILL.md` concise and put detailed information in references.

Skills are registered in `plugin.json` under the `skills` array. The existing `"./skills/"` entry auto-discovers all skill directories, so you just need to create the directory.

## Contributing Commands

Commands live in `commands/` as standalone `.md` files. They are invoked as `/opik:<filename>` (without the `.md` extension).

```
commands/
└── my-command.md           # Invoked as /opik:my-command
```

Commands support YAML frontmatter for metadata:

```yaml
---
description: Short description of what the command does
argument-hint: [optional argument format hint]
allowed-tools:
  - Read
  - Write
  - Edit
  - Bash
---
```

Commands are auto-discovered via the `"./commands/"` entry in the `skills` array of `plugin.json`.

**Important:** Do not use a `name` field in the frontmatter. The command name is derived from the filename.

## Contributing Agents

Agents live in `agents/` as `.md` files. Unlike skills and commands, each agent must be explicitly listed in `plugin.json`:

```json
{
  "agents": [
    "./agents/agent-reviewer.md",
    "./agents/my-new-agent.md"
  ]
}
```

## Building the Tracing Logger

The session tracing logger is a Go binary that hooks into Claude Code events and sends traces to Opik.

```bash
make build        # Build for all platforms (darwin/linux, amd64/arm64)
make build-local  # Build for current platform only
```

Binaries are output to `bin/`.

## Submitting a PR

1. Fork the repository
2. Create a feature branch (`git checkout -b my-feature`)
3. Make your changes
4. Test locally by reinstalling the plugin
5. Commit your changes (`git commit -m "Add my feature"`)
6. Push to your fork (`git push origin my-feature`)
7. Open a Pull Request

### Guidelines

- Keep PRs focused on a single change
- Test your changes by installing the plugin locally
- Update the README if you add new commands, skills, or agents
- Follow existing patterns and conventions in the codebase
