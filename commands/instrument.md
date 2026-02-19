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
- Where the entry point is (the function that receives user input)

## Step 3: Apply the Correct Integration

Using the patterns from the skill, add the appropriate Opik integration. Key principles:

1. **Trace at the entry point** - The outermost function that receives input must be traced first (critical for replay capability)
2. **Use framework integrations when available** - e.g., `track_openai()` instead of manual `@opik.track`
3. **Don't double-wrap** - If using an integration, don't also add decorators to the same calls
4. **Add flush for scripts** - Short-lived scripts need `client.flush()` before exit
5. **Use correct span types** - `general`, `llm`, `tool`, `retrieval`, `guardrail`

## Step 4: Report Changes

After instrumenting:
1. Explain what integration was added and why
2. Show the key changes made
3. Remind user to install and configure:
   - Python: `pip install opik && opik configure`
   - TypeScript: `npm install opik` (plus integration packages like `opik-openai`)
