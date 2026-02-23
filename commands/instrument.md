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
  - EnterPlanMode
  - ExitPlanMode
model: sonnet
---

# Add Opik Observability

Add tracing to the user's code so their LLM application is observable in Opik.

**User request:** $ARGUMENTS

## Step 1: Load the Skills

Use the Skill tool to load BOTH of these skills before doing anything else:

1. **`opik`** — Opik SDK reference: all integrations, tracing patterns, span types, code snippets
2. **`agent-ops`** — Agent architecture patterns, evaluation, what to trace and why

Load them both now. Do not proceed until both are loaded.

## Step 2: Enter Plan Mode

Use the `EnterPlanMode` tool to enter plan mode before making any changes. You will use plan mode to do discovery and build an instrumentation plan for the user to approve.

## Step 3: Discover Frameworks from Dependencies (Do This FIRST)

**Do NOT rely only on import statements.** Code may use dynamic imports (`__import__`, `importlib`), factory patterns, or lazy loading that makes frameworks invisible to import scanning.

Instead, start by reading dependency manifests to build a checklist of frameworks that MUST be instrumented:

1. **Read dependency files** — check `requirements.txt`, `pyproject.toml`, `setup.py`, `setup.cfg`, `Pipfile`, `package.json` (for TypeScript/Node)
2. **Build a framework checklist** — for each dependency that has an Opik integration (OpenAI, Anthropic, LangChain, CrewAI, LlamaIndex, etc.), add it to your checklist
3. **Note ALL languages** — if the project has both Python files AND TypeScript/JavaScript files (check for `package.json`, `tsconfig.json`, `*.ts`, `*.js`), you must instrument BOTH languages

This checklist is your source of truth. Every framework on it must be accounted for by the end.

## Step 4: Trace the Agent Flow

Now read the code to understand how it actually works. **Follow the execution flow**, don't just scan files in isolation:

1. **Find entry points** — look for `if __name__ == "__main__"`, CLI commands, HTTP handlers, exported functions. There may be MULTIPLE entry points.
2. **Trace the call graph** — from each entry point, follow function calls to understand the full execution path. Read every file that gets called.
3. **Find where each framework on your checklist is actually used** — it may be behind factories, registries, decorators, proxies, or dynamic imports. Search for:
   - The framework's package name in strings (e.g., `"openai"`, `"anthropic"`, `"crewai"` as arguments to `__import__()` or `importlib.import_module()`)
   - Class names from the framework (e.g., `OpenAI`, `Anthropic`, `ChatOpenAI`, `Agent`, `Crew`)
   - If you can't find where a dependency from the checklist is used, search the entire codebase for its package name as a string
4. **Identify existing tracing** — check if there's already tracing code. Verify it actually sends to Opik (not a homegrown stub or different tracing system). If it's fake or non-Opik, replace it.

## Step 5: Design the Trace Structure

Before deciding what integration to use where, **map out what a single trace should look like** for one user request. A single request to the agent should produce exactly ONE trace with nested spans — never multiple disconnected traces.

Draw out the trace tree based on the call graph you traced in Step 4:

```
Trace: "agent_name" (general)
├── Span: "step_1" (tool) — e.g., search, retrieval
│   └── Span: "llm_call" (llm) — e.g., embedding, completion
├── Span: "step_2" (llm) — e.g., summarize
└── Span: "step_3" (llm) — e.g., synthesize final answer
```

For each node in the tree, decide:
- **Is it in the same process as its parent?** → Use framework integration (`track_openai()`, `OpikTracer`, etc.) or `@opik.track` — these automatically nest under the parent trace.
- **Is it in a subprocess, separate service, or different language?** → The framework wrapper pattern (`trackOpenAI()`, `track_openai()`) will NOT nest under the parent — it will create separate top-level traces. Instead, use **manual tracing** with the `Opik` client to create spans explicitly. This keeps everything under one trace.

## Step 6: Write the Instrumentation Plan

Write a plan that covers:

1. **The trace tree** — show the expected trace structure for a single request, with span names and types
2. **Framework checklist with locations** — for each framework, where it's used and what Opik integration to apply
3. **Process boundaries** — identify any subprocess, service, or language boundaries in the trace tree, and note where manual tracing is needed instead of framework wrappers
4. **Entry points** — which files are entry points and need `opik.flush_tracker()` or `client.flush()`
5. **Functions to trace** — which functions get `@opik.track` and with what span type
6. **Existing tracing to replace** — any non-Opik tracing code to swap out
7. **Gaps** — any frameworks from dependencies you couldn't find usage for (flag these to the user)

Exit plan mode and get user approval before making changes.

## Step 7: Apply the Correct Integration

**Follow the trace tree from Step 5.** For each node, apply the integration pattern that matches its position in the tree.

**Before writing any integration code, read the exact reference file for the language/framework you're about to instrument.** Do NOT guess import paths or API patterns from memory. Use the Read tool on the relevant reference:
- Python integrations → read `references/integrations.md` from the `opik` skill
- Python tracing → read `references/tracing-python.md` from the `opik` skill
- TypeScript → read `references/tracing-typescript.md` from the `opik` skill

Copy the exact import paths and usage patterns from the reference. Key principles:

1. **Follow the trace tree** — Each node in your trace tree from Step 5 tells you what integration pattern to use. Nodes in the same process as their parent use framework wrappers; nodes across process boundaries use manual `Opik` client tracing with explicit spans.
2. **Trace key functions** — Add `@opik.track` to functions you want visibility into
3. **Use framework integrations when available** — e.g., `track_openai()` instead of manual `@opik.track` — but only in the main process where a parent trace exists
4. **Don't double-wrap** — If using an integration, don't also add decorators to the same calls
5. **Add flush for scripts** — Short-lived scripts need flushing before exit to ensure traces are sent. Use `opik.flush_tracker()` when using `@opik.track` decorators, or `client.flush()` when using the `Opik()` client directly. For TypeScript, use `await client.flush()`.
6. **Use correct span types** — `general`, `llm`, `tool`, `guardrail` (these are the ONLY valid types — do NOT use `retrieval` or any other type)
7. **Instrument ALL languages** — if the project has TypeScript files that make LLM calls, instrument them too
8. **Set a project name** — Use the application or repo name as the Opik project name so traces don't end up in "Default Project". For Python: `@opik.track(project_name="my-app")` on the top-level trace, or set `OPIK_PROJECT_NAME` env var. For TypeScript: `new Opik({ projectName: "my-app" })`. Use the same project name across all languages.

## Step 8: Install Dependencies

**If you added Opik packages to dependency files, install them now.** Do not leave this for the user — the code will be broken until dependencies are installed.

1. **Detect the project's package manager and use it:**
   - If `uv.lock` or `.python-version` exists → `uv add opik`
   - If `poetry.lock` exists → `poetry add opik`
   - If `Pipfile` exists → `pipenv install opik`
   - If `requirements.txt` or `pyproject.toml` with no lock file → `pip install opik`
   - If `package.json` (Node/TS) → check for `yarn.lock` (yarn), `pnpm-lock.yaml` (pnpm), otherwise `npm install`
2. **Install for every language in the project** — a Python+TypeScript project needs both `pip install`/`uv add` AND `npm install`
3. **If you modified a dependency file but didn't add Opik** (e.g., only added code changes), you still need to install `opik` if it wasn't already a dependency

## Step 9: Validate the Changes

Verify that the instrumented code can actually load and run without import errors:

1. **For Python** — run a quick import check: `python -c "from <entry_module> import <entry_function>"` to confirm no `ImportError` or `ModuleNotFoundError`
2. **For TypeScript** — run `npx tsx --eval "import '<entry_file>'"` or `npx tsc --noEmit` to confirm no module resolution errors
3. **If validation fails** — fix the issue (missing dependency, wrong import path, etc.) before reporting success
4. **Check configuration consistency** — if the project uses Opik in multiple languages, ensure the project name is the same everywhere and note any env var differences the user needs to set

## Step 10: Verify and Report

After instrumenting:

1. **Run the checklist** — compare your framework checklist from Step 3 against what you actually instrumented. For each framework, confirm it's covered or explain why not:
   - Covered: `openai` — wrapped with `track_openai()` in `providers/oai.py`
   - Covered: `langchain` — added `OpikTracer` callback in `tools/summarize.py`
   - NOT covered: `some-framework` — could not find where it's used in the codebase (ask user)
2. **Explain what was added and why**
3. **Show the key changes made**
4. **List any configuration the user still needs to set up** (e.g., `opik configure` if not already configured, environment variables)
