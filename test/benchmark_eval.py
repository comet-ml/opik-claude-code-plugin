"""
Babadook Benchmark Evaluation

Uses Opik SDK to evaluate /opik:instrument results against the adversarial
babadook agent. Single dataset item (the instrumented codebase), multiple
metrics each grading a different quality dimension.
"""

import argparse
import json
import sys
from pathlib import Path
from typing import Any

from opik import Opik
from opik.evaluation import evaluate
from opik.evaluation.metrics import BaseMetric
from opik.evaluation.metrics.score_result import ScoreResult

# ── Framework expectations ──────────────────────────────────────────

FRAMEWORKS = [
    {"name": "openai_python", "file": "providers/oai.py", "pattern": "track_openai"},
    {"name": "anthropic", "file": "providers/claude.py", "pattern": "track_anthropic"},
    {"name": "langchain", "file": "tools/summarize.py", "pattern": "OpikTracer"},
    {"name": "langgraph", "file": "tools/graph_flow.py", "pattern": "OpikTracer"},
    {"name": "google_adk", "file": "tools/adk_agent.py", "pattern": "track_adk"},
    {"name": "crewai", "file": "tools/crew.py", "pattern": "track_crewai"},
    {"name": "openai_typescript", "file": "tools/search.ts", "pattern": "opik"},
]


# ── Opik REST helpers ───────────────────────────────────────────────

class OpikAPI:
    """Wrapper around Opik SDK for trace/span queries."""

    def __init__(self, project_name: str, fallback_projects: list[str] | None = None):
        self.project_name = project_name
        self._fallback_projects = fallback_projects or []
        self._client = Opik()
        self._traces = None
        self._spans = None
        self._resolved_project = project_name

    def get_traces(self):
        if self._traces is not None:
            return self._traces
        # Try primary project first, then fallbacks
        for project in [self.project_name] + self._fallback_projects:
            try:
                traces = self._client.search_traces(project_name=project, max_results=100)
                if traces:
                    self._traces = traces
                    self._resolved_project = project
                    if project != self.project_name:
                        print(f"  [INFO] Traces found under '{project}' (not '{self.project_name}')")
                    return self._traces
            except Exception as e:
                print(f"  [WARN] search_traces for '{project}' failed: {e}")
                continue
        self._traces = []
        return self._traces

    def get_spans(self):
        if self._spans is not None:
            return self._spans
        traces = self.get_traces()
        all_spans = []
        for trace in traces:
            try:
                spans = self._client.search_spans(
                    project_name=self._resolved_project,
                    trace_id=trace.id,
                    max_results=200,
                )
                all_spans.extend(spans)
            except Exception as e:
                print(f"  [WARN] search_spans failed for trace {trace.id}: {e}")
                continue
        self._spans = all_spans
        return self._spans

    def get_span_tree(self) -> str:
        """Build an indented span tree for LLM judge evaluation."""
        traces = self.get_traces()
        if not traces:
            return "(no traces found)"

        spans = self.get_spans()
        lines = []

        for trace in traces:
            lines.append(f"Trace: {trace.name} (id={trace.id})")

            trace_spans = [s for s in spans if s.trace_id == trace.id]
            children: dict[str, list] = {}
            roots = []
            for s in trace_spans:
                pid = s.parent_span_id
                if pid:
                    children.setdefault(pid, []).append(s)
                else:
                    roots.append(s)

            def render(span, depth=1):
                indent = "  " * depth
                lines.append(f"{indent}- {span.name} (type={span.type})")
                for child in children.get(span.id, []):
                    render(child, depth + 1)

            for root in roots:
                render(root)

        return "\n".join(lines)


# ── Metrics ─────────────────────────────────────────────────────────

class FrameworkDetectionMetric(BaseMetric):
    """Checks all 7 frameworks for correct Opik integration patterns.
    Returns fraction found (e.g. 5/7 = 0.71)."""

    name = "framework_detection"

    def __init__(self, babadook_dir: str):
        super().__init__()
        self.babadook_dir = Path(babadook_dir)

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        found = []
        missing = []

        for fw in FRAMEWORKS:
            target = self.babadook_dir / fw["file"]
            pattern = fw["pattern"]

            if target.exists() and pattern in target.read_text():
                found.append(fw["name"])
                continue

            # Fallback: search all source files
            hit = False
            for ext in ("*.py", "*.ts"):
                for f in self.babadook_dir.rglob(ext):
                    try:
                        if pattern in f.read_text():
                            hit = True
                            break
                    except Exception:
                        continue
                if hit:
                    break

            if hit:
                found.append(f"{fw['name']} (relocated)")
            else:
                missing.append(fw["name"])

        score = len(found) / len(FRAMEWORKS)
        reason = f"Found {len(found)}/{len(FRAMEWORKS)}: {', '.join(found)}"
        if missing:
            reason += f" | Missing: {', '.join(missing)}"
        return ScoreResult(name=self.name, value=score, reason=reason)


class TraceCountMetric(BaseMetric):
    """Expects 1-2 traces (not 0, not 6+)."""

    name = "trace_count"

    def __init__(self, api: OpikAPI):
        super().__init__()
        self.api = api

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        traces = self.api.get_traces()
        count = len(traces)
        if 1 <= count <= 2:
            return ScoreResult(name=self.name, value=1.0, reason=f"{count} trace(s) — expected 1-2")
        if count == 0:
            return ScoreResult(name=self.name, value=0.0, reason="No traces found")
        return ScoreResult(name=self.name, value=0.5, reason=f"{count} traces — expected 1-2")


class SpanCountMetric(BaseMetric):
    """Expects >= 15 spans (baseline was ~25)."""

    name = "span_count"

    def __init__(self, api: OpikAPI):
        super().__init__()
        self.api = api

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        spans = self.api.get_spans()
        count = len(spans)
        if count >= 15:
            return ScoreResult(name=self.name, value=1.0, reason=f"{count} spans (>= 15)")
        if count >= 5:
            return ScoreResult(name=self.name, value=0.5, reason=f"{count} spans (< 15)")
        return ScoreResult(name=self.name, value=0.0, reason=f"{count} spans (too few)")


class ProjectNameMetric(BaseMetric):
    """Traces should appear under the correct project, not 'Default Project'."""

    name = "project_name"

    def __init__(self, api: OpikAPI):
        super().__init__()
        self.api = api

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        traces = self.api.get_traces()
        if not traces:
            return ScoreResult(name=self.name, value=0.0, reason=f"No traces under '{self.api.project_name}'")
        resolved = self.api._resolved_project
        if resolved == self.api.project_name:
            return ScoreResult(name=self.name, value=1.0, reason=f"Traces found under '{resolved}'")
        if resolved == "Default Project":
            return ScoreResult(name=self.name, value=0.0, reason=f"Traces in 'Default Project' — no project name set")
        return ScoreResult(name=self.name, value=0.5, reason=f"Traces under '{resolved}' (expected '{self.api.project_name}' via OPIK_PROJECT_NAME env var)")


class LLMSpanMetric(BaseMetric):
    """At least one span with type=llm."""

    name = "llm_spans"

    def __init__(self, api: OpikAPI):
        super().__init__()
        self.api = api

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        spans = self.api.get_spans()
        llm_spans = [s for s in spans if s.type == "llm"]
        if llm_spans:
            return ScoreResult(name=self.name, value=1.0, reason=f"{len(llm_spans)} LLM span(s)")
        return ScoreResult(name=self.name, value=0.0, reason="No LLM spans found")


class TraceStructureJudge(BaseMetric):
    """LLM-as-judge that evaluates overall trace quality."""

    name = "trace_structure_judge"

    def __init__(self, api: OpikAPI):
        super().__init__()
        self.api = api

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        span_tree = self.api.get_span_tree()

        if span_tree == "(no traces found)":
            return ScoreResult(name=self.name, value=0.0, reason="No traces to evaluate")

        prompt = f"""You are evaluating the observability quality of an instrumented AI agent's traces.

The agent is a multi-step research agent that:
1. Searches for information (may use a TypeScript subprocess)
2. Summarizes results (uses LangChain)
3. Synthesizes a final answer (uses OpenAI or Anthropic)

Here is the full trace/span tree:

{span_tree}

Evaluate the trace on these criteria (score 0.0-1.0):
1. Is the trace hierarchy logical? (parent-child relationships make sense)
2. Are span names descriptive and meaningful?
3. Is the trace complete? (covers the search → summarize → synthesize flow)
4. Are cross-process boundaries handled? (e.g., TypeScript subprocess traced?)
5. Are there redundant/duplicate spans?

Respond with ONLY a JSON object:
{{"score": <float 0.0-1.0>, "reasoning": "<brief explanation>"}}"""

        try:
            import openai
            client = openai.OpenAI()
            response = client.chat.completions.create(
                model="gpt-4o-mini",
                messages=[{"role": "user", "content": prompt}],
                temperature=0,
            )
            content = response.choices[0].message.content.strip()
            if content.startswith("```"):
                content = content.split("\n", 1)[1].rsplit("```", 1)[0].strip()
            result = json.loads(content)
            score = float(result.get("score", 0.0))
            reasoning = result.get("reasoning", "No reasoning provided")
            return ScoreResult(name=self.name, value=score, reason=reasoning)
        except Exception as e:
            return ScoreResult(name=self.name, value=0.5, reason=f"LLM judge error (defaulting to 0.5): {e}")


class AgentFunctionalityMetric(BaseMetric):
    """Agent should exit 0."""

    name = "agent_functionality"

    def __init__(self, agent_exit_code: int):
        super().__init__()
        self.agent_exit_code = agent_exit_code

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        if self.agent_exit_code == 0:
            return ScoreResult(name=self.name, value=1.0, reason="Agent exited successfully (code 0)")
        return ScoreResult(name=self.name, value=0.0, reason=f"Agent crashed (exit code {self.agent_exit_code})")


class DependencyMetric(BaseMetric):
    """opik should be in requirements.txt/pyproject.toml and package.json."""

    name = "dependency_management"

    def __init__(self, babadook_dir: str):
        super().__init__()
        self.babadook_dir = Path(babadook_dir)

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        found = []
        missing = []

        # Python deps
        py_found = False
        for dep_file in ("requirements.txt", "pyproject.toml"):
            path = self.babadook_dir / dep_file
            if path.exists() and "opik" in path.read_text():
                found.append(dep_file)
                py_found = True
                break
        if not py_found:
            missing.append("python deps")

        # JS deps
        pkg_json = self.babadook_dir / "package.json"
        if pkg_json.exists():
            if "opik" in pkg_json.read_text():
                found.append("package.json")
            else:
                missing.append("package.json")

        if not missing:
            return ScoreResult(name=self.name, value=1.0, reason=f"opik in: {', '.join(found)}")
        if found:
            return ScoreResult(name=self.name, value=0.5, reason=f"Found: {', '.join(found)} | Missing: {', '.join(missing)}")
        return ScoreResult(name=self.name, value=0.0, reason=f"opik missing from: {', '.join(missing)}")


class FakeInstrumentationMetric(BaseMetric):
    """No remaining `from tools._instrument import` in Python files."""

    name = "fake_instrumentation"

    def __init__(self, babadook_dir: str):
        super().__init__()
        self.babadook_dir = Path(babadook_dir)

    def score(self, output: dict[str, Any], **kwargs) -> ScoreResult:
        offending = []
        for py_file in self.babadook_dir.rglob("*.py"):
            if py_file.name == "_instrument.py":
                continue
            try:
                if "from tools._instrument import" in py_file.read_text():
                    offending.append(str(py_file.relative_to(self.babadook_dir)))
            except Exception:
                continue

        if not offending:
            return ScoreResult(name=self.name, value=1.0, reason="No remaining fake _instrument imports")
        return ScoreResult(name=self.name, value=0.0, reason=f"Fake imports in: {', '.join(offending)}")


# ── Evaluation runner ───────────────────────────────────────────────

def evaluation_task(input: str, **kwargs) -> dict[str, Any]:
    """Passthrough — the input is the babadook code path."""
    return {"output": {"code_path": input}}


def main():
    parser = argparse.ArgumentParser(description="Babadook benchmark evaluation")
    parser.add_argument("--project-name", required=True)
    parser.add_argument("--experiment-name", required=True)
    parser.add_argument("--babadook-dir", required=True)
    parser.add_argument("--agent-exit-code", type=int, required=True)
    args = parser.parse_args()

    # Setup Opik API helper — try the benchmark project name first, then common
    # names the instrument command may have hardcoded
    repo_name = Path(args.babadook_dir).name.split("-benchmark")[0]
    fallbacks = [repo_name, "babadook", "Default Project"]
    api = OpikAPI(args.project_name, fallback_projects=fallbacks)

    # Create fresh dataset (delete old one to avoid stale items)
    client = Opik()
    try:
        client.delete_dataset(name="babadook-benchmark")
    except Exception:
        pass
    dataset = client.get_or_create_dataset(name="babadook-benchmark")
    dataset.insert([{"input": args.babadook_dir}])

    metrics = [
        FrameworkDetectionMetric(args.babadook_dir),
        TraceCountMetric(api),
        SpanCountMetric(api),
        ProjectNameMetric(api),
        LLMSpanMetric(api),
        TraceStructureJudge(api),
        AgentFunctionalityMetric(args.agent_exit_code),
        DependencyMetric(args.babadook_dir),
        FakeInstrumentationMetric(args.babadook_dir),
    ]

    print(f"\nRunning evaluation: {args.experiment_name}")
    print(f"Dataset: 1 item (babadook codebase)")
    print(f"Metrics: {len(metrics)}")
    print(f"Project: {args.project_name}\n")

    results = evaluate(
        experiment_name=args.experiment_name,
        dataset=dataset,
        task=evaluation_task,
        scoring_metrics=metrics,
    )

    # Compute aggregate
    total_score = 0.0
    count = 0
    for result in results.test_results:
        for sr in result.score_results:
            if sr.value is not None:
                total_score += sr.value
                count += 1

    avg = total_score / count if count else 0.0

    print(f"\n{'='*50}")
    print(f"  Benchmark Results")
    print(f"{'='*50}")
    print(f"  Metrics scored: {count}")
    print(f"  Total:          {total_score:.1f}/{count}")
    print(f"  Average:        {avg:.2f}")
    print(f"  Threshold:      0.70")
    print(f"  Result:         {'PASS' if avg >= 0.7 else 'FAIL'}")
    print(f"{'='*50}\n")

    sys.exit(0 if avg >= 0.7 else 1)


if __name__ == "__main__":
    main()
