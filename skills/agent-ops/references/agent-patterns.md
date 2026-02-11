# Agent Architecture Patterns

Best practices for building, evaluating, and optimizing AI agents with Opik.

## The Agent Lifecycle

Building production-grade agents requires:
1. **Observability** - Understand what your agent is doing
2. **Evaluation** - Measure performance systematically
3. **Optimization** - Improve based on data

## Start with Observability

Before evaluating, make your agent's behavior transparent.

### Basic Agent Tracing

```python
import opik

@opik.track
def plan_action(query: str) -> dict:
    """Agent planning step"""
    return {"action": "search", "params": {"query": query}}

@opik.track(type="tool")
def execute_tool(action: dict) -> str:
    """Tool execution"""
    if action["action"] == "search":
        return search_web(action["params"]["query"])

@opik.track
def generate_response(query: str, tool_results: str) -> str:
    """Final response generation"""
    return llm_call(f"Query: {query}\nResults: {tool_results}")

@opik.track(name="research_agent")
def agent(query: str) -> str:
    plan = plan_action(query)
    results = execute_tool(plan)
    return generate_response(query, results)
```

### What to Trace

| Component | Span Type | Key Data |
|-----------|-----------|----------|
| Planning | `general` | Reasoning steps, decisions |
| Tool calls | `tool` | Tool name, parameters, results |
| LLM calls | `llm` | Prompt, response, tokens |
| Retrieval | `retrieval` | Query, documents |
| Validation | `guardrail` | Check results, pass/fail |

## Evaluating Agents

Agent evaluation goes beyond final outputs—you need to assess the journey.

### End-to-End Evaluation

Evaluate the final response quality:

```python
from opik.evaluation import evaluate
from opik.evaluation.metrics import AnswerRelevance, Hallucination

def agent_task(dataset_item):
    response = agent(dataset_item["input"])
    return {"output": response}

results = evaluate(
    experiment_name="agent-e2e-v1",
    dataset=dataset,
    task=agent_task,
    scoring_metrics=[
        AnswerRelevance(),
        Hallucination()
    ]
)
```

### Step-Level Evaluation

Evaluate individual agent decisions:

#### Tool Selection Evaluation

```python
from opik.evaluation.metrics import BaseMetric, ScoreResult

class ToolSelectionQuality(BaseMetric):
    def __init__(self):
        self.name = "tool_selection_quality"

    def score(self, tool_calls, expected_tool_calls, **kwargs):
        actual = tool_calls[0]["function_name"] if tool_calls else None
        expected = expected_tool_calls[0]["function_name"] if expected_tool_calls else None

        if actual == expected:
            return ScoreResult(
                name=self.name,
                value=1.0,
                reason=f"Correct tool: {actual}"
            )
        return ScoreResult(
            name=self.name,
            value=0.0,
            reason=f"Expected {expected}, got {actual}"
        )
```

#### Trajectory Evaluation

Use `task_span` parameter for trajectory access:

```python
from opik.evaluation.metrics import BaseMetric, ScoreResult
from opik.message_processing.emulation.models import SpanModel

class StrictToolAdherenceMetric(BaseMetric):
    def __init__(self):
        self.name = "strict_tool_adherence"

    def find_tools(self, task_span: SpanModel) -> list:
        """Extract tool names from span hierarchy"""
        tools = []

        def extract(spans):
            for span in spans:
                if span.type == "tool":
                    tools.append(span.name)
                if span.spans:
                    extract(span.spans)

        if task_span.spans:
            extract(task_span.spans)
        return tools

    def score(self, task_span: SpanModel, expected_tool: list, **kwargs):
        actual = self.find_tools(task_span)

        if actual == expected_tool:
            return ScoreResult(
                name=self.name,
                value=1.0,
                reason=f"Correct trajectory: {actual}"
            )
        return ScoreResult(
            name=self.name,
            value=0.0,
            reason=f"Expected {expected_tool}, got {actual}"
        )
```

### Built-in Agent Metrics

| Metric | Description |
|--------|-------------|
| `AgentTaskCompletion` | Did the agent fulfill its task? |
| `AgentToolCorrectness` | Were tools used with correct parameters? |
| `TrajectoryAccuracy` | Did actions match expected sequence? |

## Evaluation Dataset Design

### Tool Selection Dataset

```python
dataset.insert([
    {
        "input": "What is 25 * 17?",
        "expected_tool": ["calculator"]
    },
    {
        "input": "What's the weather in Paris?",
        "expected_tool": ["weather_api"]
    },
    {
        "input": "Tell me a joke",
        "expected_tool": []  # No tool needed
    }
])
```

### Multi-Step Dataset

```python
dataset.insert([
    {
        "input": "Book a flight and hotel for NYC",
        "expected_trajectory": [
            {"tool": "search_flights", "params": {"destination": "NYC"}},
            {"tool": "search_hotels", "params": {"city": "NYC"}},
            {"tool": "book_flight"},
            {"tool": "book_hotel"}
        ]
    }
])
```

## Evaluating Different Components

### What to Evaluate

| Component | Metrics | Dataset Fields |
|-----------|---------|----------------|
| Router/Planner | Tool selection, plan quality | Expected tools/plan |
| Tools | Output accuracy, error rate | Expected tool output |
| Memory/RAG | Relevance, recall | Expected context |
| Response | Quality, hallucination | Expected response |

### Component-Specific Evaluation

```python
# Router evaluation
router_results = evaluate(
    experiment_name="router-v1",
    dataset=router_dataset,
    task=router_task,
    scoring_metrics=[ToolSelectionQuality()]
)

# Tool evaluation
tool_results = evaluate(
    experiment_name="tools-v1",
    dataset=tool_dataset,
    task=tool_task,
    scoring_metrics=[ExactMatch(), ErrorRate()]
)

# End-to-end evaluation
e2e_results = evaluate(
    experiment_name="agent-v1",
    dataset=e2e_dataset,
    task=agent_task,
    scoring_metrics=[
        AnswerRelevance(),
        AgentTaskCompletion(),
        TrajectoryAccuracy()
    ]
)
```

## Multi-Agent Systems

### Tracing Multi-Agent Workflows

```python
import opik

@opik.track(name="orchestrator")
def orchestrator(query: str) -> str:
    # Decide which agent to use
    agent_type = classify_query(query)

    if agent_type == "research":
        return research_agent(query)
    elif agent_type == "code":
        return code_agent(query)
    else:
        return general_agent(query)

@opik.track(name="research_agent")
def research_agent(query: str) -> str:
    # Research-specific logic
    pass

@opik.track(name="code_agent")
def code_agent(query: str) -> str:
    # Code-specific logic
    pass
```

### Evaluating Agent Routing

```python
class RoutingAccuracy(BaseMetric):
    def __init__(self):
        self.name = "routing_accuracy"

    def score(self, selected_agent, expected_agent, **kwargs):
        if selected_agent == expected_agent:
            return ScoreResult(
                name=self.name,
                value=1.0,
                reason=f"Correctly routed to {selected_agent}"
            )
        return ScoreResult(
            name=self.name,
            value=0.0,
            reason=f"Routed to {selected_agent}, expected {expected_agent}"
        )
```

## Common Anti-Patterns

### What to Watch For

1. **Tool loops**: Agent repeatedly calls same tool
2. **Hallucinated tools**: Agent invents non-existent tools
3. **Parameter errors**: Wrong types or missing required params
4. **Inefficient paths**: Taking more steps than necessary
5. **Context loss**: Forgetting information across turns

### Detection Metrics

```python
class LoopDetection(BaseMetric):
    def __init__(self, max_repeats: int = 3):
        self.name = "loop_detection"
        self.max_repeats = max_repeats

    def score(self, task_span, **kwargs):
        tools = self.find_tools(task_span)

        # Check for repeated consecutive tools
        for i in range(len(tools) - self.max_repeats + 1):
            window = tools[i:i + self.max_repeats]
            if len(set(window)) == 1:  # All same tool
                return ScoreResult(
                    name=self.name,
                    value=0.0,
                    reason=f"Detected loop: {window[0]} repeated {self.max_repeats} times"
                )

        return ScoreResult(
            name=self.name,
            value=1.0,
            reason="No loops detected"
        )
```

## Iterative Improvement

### The Evaluation Loop

1. **Run baseline evaluation**
2. **Analyze failures** - Filter to low-scoring items
3. **Identify patterns** - What's causing failures?
4. **Make improvements**:
   - Refine system prompt
   - Improve tool descriptions
   - Add/remove tools
   - Adjust parameters
5. **Re-evaluate** - Measure impact
6. **Compare experiments** - Verify improvement
7. **Repeat**

### Comparing Experiments

In the Opik UI:
1. Go to dataset experiments
2. Select experiments to compare
3. View metric differences
4. Drill into specific failures
5. Document what changed

## Best Practices

### Observability

- Trace all agent components
- Use appropriate span types
- Add metadata for filtering
- Include tool parameters and results

### Evaluation

- Start with end-to-end metrics
- Add component-level as needed
- Build datasets from production failures
- Run evaluations before deploying changes

### Optimization

- Change one variable at a time
- Track experiment configurations
- Use data to guide decisions
- Monitor production performance
