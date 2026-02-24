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

## Step 1: Load the Skills

Use the Skill tool to load BOTH of these skills before doing anything else:

1. **`opik`** — Opik SDK reference: all integrations, tracing patterns, span types, code snippets
2. **`agent-ops`** — Agent architecture patterns, evaluation, what to trace and why

Load them both now. Do not proceed until both are loaded.

## Step 2: Discover Frameworks from Dependencies (Do This FIRST)

**Do NOT rely only on import statements.** Code may use dynamic imports (`__import__`, `importlib`), factory patterns, or lazy loading that makes frameworks invisible to import scanning.

Instead, start by reading dependency manifests to build a checklist of frameworks that MUST be instrumented:

1. **Read dependency files** — check `requirements.txt`, `pyproject.toml`, `setup.py`, `setup.cfg`, `Pipfile`, `package.json` (for TypeScript/Node)
2. **Build a framework checklist** — for each dependency that has an Opik integration (OpenAI, Anthropic, LangChain, CrewAI, LlamaIndex, etc.), add it to your checklist
3. **Note ALL languages** — if the project has both Python files AND TypeScript/JavaScript files (check for `package.json`, `tsconfig.json`, `*.ts`, `*.js`), you must instrument BOTH languages

This checklist is your source of truth. Every framework on it must be accounted for by the end.

## Step 3: Trace the Agent Flow

Now read the code to understand how it actually works. **Follow the execution flow**, don't just scan files in isolation:

1. **Find entry points** — look for `if __name__ == "__main__"`, CLI commands, HTTP handlers, exported functions. There may be MULTIPLE entry points.
2. **Trace the call graph** — from each entry point, follow function calls to understand the full execution path. Read every file that gets called.
3. **Find where each framework on your checklist is actually used** — it may be behind factories, registries, decorators, proxies, or dynamic imports. Search for:
   - The framework's package name in strings (e.g., `"openai"`, `"anthropic"`, `"crewai"` as arguments to `__import__()` or `importlib.import_module()`)
   - Class names from the framework (e.g., `OpenAI`, `Anthropic`, `ChatOpenAI`, `Agent`, `Crew`)
   - If you can't find where a dependency from the checklist is used, search the entire codebase for its package name as a string
4. **Identify existing tracing** — check if there's already tracing code. Verify it actually sends to Opik (not a homegrown stub or different tracing system). If it's fake or non-Opik, replace it.

## Step 4: Design the Trace Structure

Before deciding what integration to use where, **map out what a single trace should look like** for one user request. A single request to the agent should produce exactly ONE trace with nested spans — never multiple disconnected traces.

Draw out the trace tree based on the call graph you traced in Step 3:

```
Trace: "agent_name" (general)
├── Span: "step_1" (tool) — e.g., search, retrieval
│   └── Span: "llm_call" (llm) — e.g., embedding, completion
├── Span: "step_2" (llm) — e.g., summarize
└── Span: "step_3" (llm) — e.g., synthesize final answer
```

For each node in the tree, decide:
- **Is it in the same process as its parent?** → Use framework integration (`track_openai()`, `OpikTracer`, etc.) or `@opik.track` — these automatically nest under the parent trace.
- **Is it in a subprocess, separate service, or different language?** → The framework wrapper pattern (`trackOpenAI()`, `track_openai()`) will NOT nest under the parent — it will create separate top-level traces. You MUST propagate the trace ID and parent span ID across the process boundary so the child process creates spans under the same trace. How to propagate depends on the IPC mechanism:
  - **HTTP services:** Use `opik.opik_context.get_distributed_trace_headers()` on the caller side to get headers, pass them in the HTTP request, and use `distributed_headers=` on the receiving side (see `references/tracing-python.md`).
  - **Subprocesses (stdin/stdout, pipes, message queues):** On the caller side, read `opik.opik_context.get_current_trace_data().id` and `opik.opik_context.get_current_span_data().id`, then include `opik_trace_id` and `opik_parent_span_id` in the message payload. On the child side, use the `Opik` client to create a trace handle with `id=opik_trace_id`, then create spans with `parentSpanId=opik_parent_span_id`. Do NOT call `trace.end()` in the child — the parent process owns the trace lifecycle.
  - **Environment variables:** For fire-and-forget subprocesses, pass `OPIK_PARENT_TRACE_ID` and `OPIK_PARENT_SPAN_ID` in the subprocess environment.

  NEVER create a new top-level trace for work that is part of an existing user request. If a component handles part of a request, its spans MUST be linked to the parent trace.

## Step 5: Apply the Correct Integration

**Follow the trace tree from Step 4.** For each node, apply the integration pattern that matches its position in the tree.

**Before writing any integration code, read the exact reference file for the language/framework you're about to instrument.** Do NOT guess import paths or API patterns from memory. Use the Read tool on the relevant reference:
- Python integrations → read `references/integrations.md` from the `opik` skill
- Python tracing → read `references/tracing-python.md` from the `opik` skill
- TypeScript → read `references/tracing-typescript.md` from the `opik` skill

Copy the exact import paths and usage patterns from the reference. Key principles:

1. **Follow the trace tree** — Each node in your trace tree from Step 4 tells you what integration pattern to use. Nodes in the same process as their parent use framework wrappers; nodes across process boundaries use manual `Opik` client tracing with explicit spans.
2. **Trace key functions** — Add `@opik.track` to functions you want visibility into
3. **Use framework integrations when available** — e.g., `track_openai()` instead of manual `@opik.track` — but only in the main process where a parent trace exists
4. **Don't double-wrap** — If using an integration, don't also add decorators to the same calls
5. **Add flush for scripts** — Short-lived scripts need flushing before exit to ensure traces are sent. Use `opik.flush_tracker()` when using `@opik.track` decorators, or `client.flush()` when using the `Opik()` client directly. For TypeScript, use `await client.flush()`.
6. **Use correct span types** — `general`, `llm`, `tool`, `guardrail` (these are the ONLY valid types — do NOT use `retrieval` or any other type)
7. **Instrument ALL languages** — if the project has TypeScript files that make LLM calls, instrument them too
8. **Set a default project name via env var** — Use `OPIK_PROJECT_NAME` env var so traces don't end up in "Default Project". Do NOT hardcode `project_name=` in decorators or client constructors — this prevents users from overriding the project at runtime. Instead, set `os.environ.setdefault("OPIK_PROJECT_NAME", "app-name")` near the entry point, or document that users should set the env var. For TypeScript, use `process.env.OPIK_PROJECT_NAME || "app-name"` when creating the client.

## Step 6: Install Dependencies

**If you added Opik packages to dependency files, install them now.** Do not leave this for the user — the code will be broken until dependencies are installed.

1. **Detect the project's package manager and use it:**
   - If `uv.lock` or `.python-version` exists → `uv add opik`
   - If `poetry.lock` exists → `poetry add opik`
   - If `Pipfile` exists → `pipenv install opik`
   - If `requirements.txt` or `pyproject.toml` with no lock file → `pip install opik`
   - If `package.json` (Node/TS) → check for `yarn.lock` (yarn), `pnpm-lock.yaml` (pnpm), otherwise `npm install`
2. **Install for every language in the project** — a Python+TypeScript project needs both `pip install`/`uv add` AND `npm install`
3. **If you modified a dependency file but didn't add Opik** (e.g., only added code changes), you still need to install `opik` if it wasn't already a dependency
4. **If installation fails due to peer dependency conflicts, do NOT force-install** (no `--legacy-peer-deps`, `--force`, etc.). A dependency conflict means the packages are incompatible with the project's existing dependencies. Instead, skip instrumenting that language/framework and report it as NOT covered in Step 8 with the specific version conflict.

## Step 7: Validate the Changes

Verify that the instrumented code can actually load and run without import errors:

1. **For Python** — run a quick import check: `python -c "from <entry_module> import <entry_function>"` to confirm no `ImportError` or `ModuleNotFoundError`
2. **For TypeScript** — run BOTH of these checks:
   - `npx tsc --noEmit` to confirm no static type errors
   - `npx tsx --eval "import '<entry_file>'"` to confirm no runtime module resolution errors (catches missing transitive dependencies, broken peer deps, etc. that type-checking alone misses)
3. **If validation fails** — fix the issue (missing dependency, wrong import path, etc.) before reporting success
4. **Check configuration consistency** — if the project uses Opik in multiple languages, ensure the project name is the same everywhere and note any env var differences the user needs to set

## Step 8: Verify and Report

After instrumenting:

1. **Verify one-trace-per-request** — trace through a single user request end-to-end and confirm that ALL spans (across all processes and languages) land in ONE trace. If any component creates a separate top-level trace, that is a bug — fix it by propagating trace context across the process boundary before reporting success.
2. **Run the checklist** — compare your framework checklist from Step 2 against what you actually instrumented. For each framework, confirm it's covered or explain why not:
   - Covered: `openai` — wrapped with `track_openai()` in `providers/oai.py`
   - Covered: `langchain` — added `OpikTracer` callback in `tools/summarize.py`
   - NOT covered: `some-framework` — could not find where it's used in the codebase (ask user)
3. **Explain what was added and why**
4. **Show the key changes made**
5. **List any configuration the user still needs to set up** (e.g., `opik configure` if not already configured, environment variables)
