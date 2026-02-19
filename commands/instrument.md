---
description: Add Opik observability to your code - automatically detects frameworks and adds the correct integration
argument-hint: [file or description of what to instrument]
allowed-tools:
  - Read
  - Write
  - Edit
  - Glob
  - Grep
  - Skill
  - Bash
model: sonnet
---

# Add Opik Observability

Add tracing to the user's code so their LLM application is observable in Opik.

**User request:** $ARGUMENTS

## Step 1: Load the Skill

First, use the Skill tool to load `agent-ops`. This gives you comprehensive knowledge of:
- All Opik integrations (OpenAI, Anthropic, LangChain, LlamaIndex, CrewAI, etc.)
- Python and TypeScript SDK patterns
- Correct span types and decorator usage
- Agent architecture best practices

## Step 2: Analyze the Code

Read the target file(s) and identify:
- What language (Python or TypeScript)
- What frameworks/libraries are used (look at imports)
- Where the main function is (the function that kicks off the workflow)

## Step 3: Apply the Correct Integration

Using the patterns from the skill, add the appropriate Opik integration. Key principles:

1. **Trace key functions** - Add `@opik.track` to functions you want visibility into
2. **Use framework integrations when available** - e.g., `track_openai()` instead of manual `@opik.track`
3. **Don't double-wrap** - If using an integration, don't also add decorators to the same calls
4. **Add flush for scripts** - Short-lived scripts need flushing before exit to ensure traces are sent. Use `opik.flush_tracker()` when using `@opik.track` decorators, or `client.flush()` when using the `Opik()` client directly. For TypeScript, use `await client.flush()`.
5. **Use correct span types** - `general`, `llm`, `tool`, `guardrail` (these are the ONLY valid types — do NOT use `retrieval` or any other type)

## Step 4: Report Changes

After instrumenting:
1. Explain what integration was added and why
2. Show the key changes made
3. Remind user to install and configure:
   - Python: `pip install opik && opik configure`
   - TypeScript: `npm install opik` (plus integration packages like `opik-openai`)
