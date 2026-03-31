# Evaluation & Metrics Guide

Comprehensive guide to evaluating LLM applications with Opik's evaluation platform.

## Why Evaluation Matters

Manual review of LLM outputs doesn't scale. Opik's evaluation platform automates quality assessment with:
- **Reproducible experiments** across datasets
- **Quantitative metrics** for objective comparison
- **Historical tracking** to measure improvement
- **Side-by-side comparison** of different approaches

## Core Concepts

### Datasets

A **dataset** is a collection of test cases for evaluating your LLM application.

Each dataset item contains:
- **Input**: The query/prompt to send to your application
- **Expected output** (optional): The ground truth or reference answer
- **Custom fields**: Any additional context needed for evaluation

### Experiments

An **experiment** is a single evaluation run that:
1. Processes each dataset item through your LLM application
2. Computes the actual output
3. Scores the output using one or more metrics
4. Logs results for analysis

## Creating Datasets

### Via Python SDK

```python
from opik import Opik

client = Opik()
dataset = client.get_or_create_dataset(name="my-evaluation-dataset")

# Insert items
dataset.insert([
    {
        "input": "What is the capital of France?",
        "expected_output": "Paris"
    },
    {
        "input": "Explain quantum computing in simple terms",
        "expected_output": "Quantum computing uses quantum mechanics..."
    }
])
```

### From Production Traces

In the Opik UI:
1. Go to your project's traces
2. Select traces you want to use
3. Click "Add to dataset" in Actions dropdown

### From CSV/JSON

Upload files directly through the Opik UI or API.

### From Pandas DataFrame

```python
import pandas as pd
from opik import Opik

client = Opik()
dataset = client.get_or_create_dataset(name="from-pandas")

df = pd.DataFrame({
    "input": ["What is ML?", "Explain AI"],
    "expected_output": ["Machine learning is...", "AI is..."]
})

dataset.insert_from_pandas(df)
```

### From JSONL Files

```python
dataset.insert_from_jsonl("path/to/data.jsonl")
```

## Dataset Versioning

Opik supports immutable dataset versions for reproducible evaluations.

### Creating Versions

```python
from opik import Opik

client = Opik()
dataset = client.get_dataset(name="my-dataset")

# Create a named version (immutable snapshot)
version = dataset.create_version(name="v1.0")

# List all versions
versions = dataset.list_versions()
for v in versions:
    print(f"{v.name}: {v.item_count} items, created {v.created_at}")
```

### Using Specific Versions

```python
# Run evaluation on a specific version
results = evaluate(
    experiment_name="test-v1",
    dataset=dataset,
    dataset_version="v1.0",  # Pin to specific version
    task=evaluation_task,
    scoring_metrics=[AnswerRelevance()]
)
```

### Version History

In the UI:
1. Go to dataset details
2. Click "Versions" tab
3. View version history with timestamps
4. Compare versions side-by-side
5. Restore or duplicate from any version

## AI Expansion (Synthetic Data)

Generate synthetic test data to expand your datasets.

### Using AI Expansion

In the Opik UI:
1. Go to your dataset
2. Click "AI Expansion"
3. Select seed examples (optional)
4. Configure expansion parameters:
   - Number of new items
   - Diversity settings
   - Topic constraints
5. Review and approve generated items

### Programmatic Expansion

```python
from opik import Opik

client = Opik()
dataset = client.get_dataset(name="my-dataset")

# Generate synthetic variations
dataset.expand(
    num_items=50,
    seed_items=dataset.get_items()[:5],  # Use 5 examples as seeds
    diversity="high",
    model="gpt-4"
)
```

## OQL: Opik Query Language

Filter datasets and traces using OQL syntax.

### Basic Syntax

```
field_name operator value
```

### Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `=` | Equals | `status = "success"` |
| `!=` | Not equals | `model != "gpt-3.5"` |
| `>`, `<`, `>=`, `<=` | Comparison | `score > 0.8` |
| `contains` | Substring match | `input contains "error"` |
| `in` | List membership | `tag in ["prod", "staging"]` |
| `exists` | Field exists | `metadata.user_id exists` |

### Combining Filters

```
# AND (implicit)
score > 0.8 model = "gpt-4"

# OR (explicit)
score > 0.9 OR model = "gpt-4"

# Parentheses for grouping
(score > 0.8 AND model = "gpt-4") OR tag = "important"
```

### Examples

```python
# Filter traces
traces = client.search_traces(
    project_name="production",
    filter='score > 0.7 AND metadata.user_type = "premium"'
)

# Filter dataset items
items = dataset.get_items(
    filter='input contains "error" AND expected_output exists'
)
```

## Annotation Queues

Create review workflows for expert human evaluation.

### Creating a Queue

In the Opik UI:
1. Go to Project > Annotation Queues
2. Click "Create Queue"
3. Configure:
   - Queue name
   - Sampling rules (all traces, percentage, or filtered)
   - Annotation schema (scores, labels, free text)
   - Assignees

### Annotation Schema

Define what reviewers evaluate:

```python
from opik import Opik

client = Opik()

# Create annotation queue
queue = client.create_annotation_queue(
    name="quality-review",
    project_name="production",
    schema={
        "scores": [
            {"name": "accuracy", "type": "numeric", "min": 0, "max": 5},
            {"name": "helpfulness", "type": "numeric", "min": 0, "max": 5}
        ],
        "labels": [
            {"name": "category", "options": ["good", "needs_work", "bad"]}
        ],
        "free_text": ["comments"]
    },
    sampling_rate=0.1  # Sample 10% of traces
)
```

### Reviewing Items

1. Go to your annotation queue
2. Items appear based on sampling rules
3. For each item:
   - View trace details
   - Apply scores and labels
   - Add comments
   - Submit annotation
4. Progress is tracked per reviewer

### Using Annotations

```python
# Get annotated traces
annotated = client.search_traces(
    project_name="production",
    filter='annotation.queue = "quality-review" AND annotation.completed = true'
)

# Export annotations for training
annotations = client.export_annotations(
    queue_name="quality-review",
    format="jsonl"
)
```

## Running Evaluations

### Basic Evaluation

```python
from opik import Opik
from opik.evaluation import evaluate
from opik.evaluation.metrics import Equals, AnswerRelevance

client = Opik()
dataset = client.get_dataset(name="my-dataset")

# Define the task (how to process each item)
def evaluation_task(dataset_item):
    # Your LLM application logic
    response = my_llm_call(dataset_item["input"])
    return {"output": response}

# Run evaluation
results = evaluate(
    experiment_name="baseline-v1",
    dataset=dataset,
    task=evaluation_task,
    scoring_metrics=[
        Equals(),              # Exact match
        AnswerRelevance()      # LLM-as-Judge
    ]
)
```

### Evaluation with Context

For RAG applications:

```python
def evaluation_task(dataset_item):
    query = dataset_item["input"]

    # Retrieve context
    context = retrieve_documents(query)

    # Generate response
    response = generate_with_context(query, context)

    return {
        "output": response,
        "context": context  # Pass to metrics
    }

results = evaluate(
    experiment_name="rag-v1",
    dataset=dataset,
    task=evaluation_task,
    scoring_metrics=[
        ContextPrecision(),
        ContextRecall(),
        Hallucination()
    ]
)
```

## Built-in Metrics (41 Total)

Opik provides 41 built-in metrics organized into categories.

### Heuristic Metrics

Deterministic, rule-based checks that don't require LLM calls:

**Text Similarity:**
- `Equals` - Exact string match
- `Contains` - Substring presence
- `RegexMatch` - Pattern matching
- `Levenshtein` - Edit distance
- `BLEU` - Translation quality (n-gram overlap)
- `ROUGE` - Summarization quality (recall-oriented)
- `BERTScore` - Semantic similarity using embeddings

**Validation:**
- `IsJson` - Valid JSON check
- `JsonSchemaMatch` - Validates against JSON schema
- `Sentiment` - Sentiment analysis (-1 to 1)

### Conversation Heuristic Metrics

For multi-turn conversation analysis:

- `ConversationCoherence` - Flow between turns
- `TopicDrift` - Measures topic consistency
- `ResponseLatency` - Tracks response timing patterns

### LLM-as-Judge Metrics

Use an LLM to evaluate semantic quality:

**Quality Assessment:**
- `AnswerRelevance` - Does the answer address the question?
- `Hallucination` - Are there unsupported claims?
- `Usefulness` - How useful is the response?
- `MeaningMatch` - Semantic equivalence
- `Moderation` - Safety and policy violations
- `GEval` - Configurable custom criteria

**RAG-Specific:**
- `ContextPrecision` - Is only relevant context used?
- `ContextRecall` - Is all relevant context used?
- `Faithfulness` - Does output align with provided context?

### Conversation LLM Metrics

For evaluating chat and dialogue quality:

- `ConversationQuality` - Overall conversation effectiveness
- `ResponseAppropriate` - Is the response fitting for the context?
- `TurnCoherence` - Logical connection between turns

### Agent-Specific Metrics

For evaluating agentic behavior:

- `AgentTaskCompletion` - Did the agent complete its task?
- `AgentToolCorrectness` - Were tools used correctly?
- `TrajectoryAccuracy` - Did the agent follow expected steps?
- `PlanningQuality` - Quality of agent's planning
- `ToolSelectionAccuracy` - Did agent pick appropriate tools?

## Using Metrics

### Simple Scoring

```python
from opik.evaluation.metrics import Hallucination

metric = Hallucination()

result = metric.score(
    input="What is the capital of France?",
    output="The capital of France is Paris. It has the Eiffel Tower.",
    context=["Paris is the capital of France."]
)

print(result.value)   # 0.0 (no hallucination)
print(result.reason)  # Explanation
```

### Custom Model for LLM Metrics

```python
from opik.evaluation.metrics import Hallucination

# Use a different LLM as judge
metric = Hallucination(model="bedrock/anthropic.claude-3-sonnet-20240229-v1:0")
```

### G-Eval: Custom Criteria

```python
from opik.evaluation.metrics import GEval

# Define custom evaluation criteria
metric = GEval(
    name="technical_accuracy",
    criteria="""
    Evaluate the technical accuracy of the response:
    1. Are technical terms used correctly?
    2. Are explanations factually accurate?
    3. Is the complexity appropriate for the audience?
    """,
    model="gpt-4"
)
```

## Custom Metrics

Create your own metrics:

```python
from opik.evaluation.metrics import BaseMetric, ScoreResult

class ResponseLengthMetric(BaseMetric):
    def __init__(self, min_length: int = 50, max_length: int = 500):
        self.name = "response_length"
        self.min_length = min_length
        self.max_length = max_length

    def score(self, output: str, **kwargs) -> ScoreResult:
        length = len(output)

        if self.min_length <= length <= self.max_length:
            return ScoreResult(
                name=self.name,
                value=1.0,
                reason=f"Length {length} is within acceptable range"
            )
        else:
            return ScoreResult(
                name=self.name,
                value=0.0,
                reason=f"Length {length} outside range [{self.min_length}, {self.max_length}]"
            )
```

## Experiment-Level Metrics

Compute aggregate metrics across all test results:

```python
def compute_experiment_scores(test_results):
    scores = [r.scores.get("accuracy", 0) for r in test_results]
    return {
        "mean_accuracy": sum(scores) / len(scores),
        "min_accuracy": min(scores),
        "pass_rate": sum(1 for s in scores if s > 0.8) / len(scores)
    }

results = evaluate(
    experiment_name="with-aggregates",
    dataset=dataset,
    task=evaluation_task,
    scoring_metrics=[AnswerRelevance()],
    experiment_scoring_functions=[compute_experiment_scores]
)
```

## Comparing Experiments

In the Opik UI:
1. Go to your dataset's experiments
2. Select experiments to compare
3. View side-by-side metrics
4. Analyze per-item differences

## Evaluation Best Practices

### Dataset Design

1. **Representative samples**: Cover edge cases and typical usage
2. **Clear expected outputs**: When possible, include ground truth
3. **Version your datasets**: Track changes over time
4. **Balance coverage**: Include examples across all use cases

### Metric Selection

1. **Start simple**: Begin with heuristic metrics
2. **Add LLM judges**: For semantic quality
3. **Custom metrics**: For domain-specific requirements
4. **Multiple metrics**: Capture different quality dimensions

### Experiment Workflow

1. **Baseline first**: Establish current performance
2. **Change one variable**: Isolate impact of changes
3. **Document config**: Track model, prompt, parameters
4. **Iterate systematically**: Use data to guide improvements

## Online Evaluation

Run metrics automatically on production traces:

### Setting Up Rules

In the Opik UI:
1. Go to Project Settings > Evaluation Rules
2. Create a new rule
3. Select the metric
4. Configure sampling (all traces or percentage)
5. Activate the rule

### Supported Online Metrics

- Answer Relevance
- Hallucination
- Moderation
- Custom LLM-as-Judge rules

## TypeScript Evaluation

```typescript
import { Opik, evaluate, Hallucination } from "opik";

const client = new Opik();
const dataset = await client.getDataset("my-dataset");

const results = await evaluate({
  experimentName: "ts-evaluation",
  dataset,
  task: async (item) => {
    const response = await myLLM(item.input);
    return { output: response };
  },
  scoringMetrics: [new Hallucination({ model: "gpt-4o" })]
});
```

## Troubleshooting

### Metrics returning unexpected scores

- Check input/output field names match metric expectations
- Verify context is passed for RAG metrics
- Review the `reason` field for explanation

### Slow evaluations

- Use batch APIs when possible
- Consider sampling large datasets
- Choose faster models for LLM-as-Judge metrics

### Inconsistent LLM-as-Judge scores

- Set `temperature=0` for deterministic results
- Use `seed` parameter when available
- Run multiple trials and average
