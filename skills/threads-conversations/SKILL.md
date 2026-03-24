---
name: threads-conversations
description: Guide for using thread_id to group multi-turn conversations, conversation metrics (session completeness, user frustration, coherence), thread evaluation, and thread lifecycle management in Opik.
---

# Threads & Conversation Tracking

Threads group related traces into coherent conversations for multi-turn agents.

## How Threads Work

- Each conversation turn = one trace
- All traces sharing a `thread_id` form a **thread**
- Threads tab shows aggregated view: first/last message, turn count, duration, total cost

## Adding thread_id

### From a session/conversation ID

```python
import opik

@opik.track(entrypoint=True, project_name="chat-agent")
def handle_message(session_id: str, message: str) -> str:
    """Handle a chat message.

    Args:
        session_id: Conversation session identifier.
        message: The user's message.
    """
    opik.update_current_trace(thread_id=session_id)
    return generate_response(session_id, message)
```

### Generating a thread ID

```python
import uuid

class ChatAgent:
    def __init__(self):
        self.thread_id = str(uuid.uuid4())

    @opik.track(entrypoint=True, project_name="chat-agent")
    def handle_message(self, message: str) -> str:
        opik.update_current_trace(thread_id=self.thread_id)
        return self.generate(message)
```

### Using opik_args

```python
result = handle_message(
    "Hello",
    opik_args={"trace": {"thread_id": "session-123"}}
)
```

## Thread Metadata (Automatic)

| Field | Description |
|-------|-------------|
| `first_message` | Input from the first trace in the thread |
| `last_message` | Output from the last trace in the thread |
| `number_of_messages` | Count of traces in the thread |
| `duration` | Time from first to last trace |
| `total_estimated_cost` | Aggregated cost across all traces |
| `status` | `active` or `inactive` (auto-close after timeout) |

## Evaluating Conversations

### Thread-Level Metrics

```python
from opik.evaluation import evaluate_threads
from opik.evaluation.metrics.conversation import (
    SessionCompletenessMetric,
    UserFrustrationMetric,
    ConversationalCoherenceMetric,
)

results = evaluate_threads(
    project_name="chat-agent",
    metrics=[
        SessionCompletenessMetric(),
        UserFrustrationMetric(),
        ConversationalCoherenceMetric(),
    ],
    trace_input_transform=lambda t: t.input["message"],
    trace_output_transform=lambda t: t.output["response"],
)
```

### Available Conversation Metrics

| Metric | What It Measures |
|--------|-----------------|
| `SessionCompletenessMetric` | Did the conversation reach resolution? |
| `UserFrustrationMetric` | Did the user show frustration signals? |
| `ConversationalCoherenceMetric` | Did the agent maintain logical consistency? |

### Thread Feedback Scores

```python
client.log_thread_feedback_scores(
    thread_id="session-123",
    scores=[
        {"name": "resolution_quality", "value": 0.85},
        {"name": "customer_satisfaction", "value": 0.9},
    ]
)
```

## Thread ID Strategies

| Strategy | Example | Best For |
|----------|---------|----------|
| Session-based | `f"session-{user_id}-{session_id}"` | Web apps with session tracking |
| UUID | `str(uuid.uuid4())` | Stateless APIs |
| Ticket-based | `f"ticket-{ticket_id}"` | Support systems |
| Timestamp | `f"{user_id}-{datetime.now().isoformat()}"` | Time-ordered analysis |

## When to Use Threads

- **Chat agents** with multi-turn conversations
- **Support bots** handling customer sessions
- **Multi-step assistants** (research, planning)
- **Claude Code sessions** (automatic via plugin)

## When NOT to Use Threads

- **Single-shot agents** (one input → one output)
- **Batch processing** (no conversation context)
- **Independent requests** (no session relationship)

## Common Pitfalls

- **Forgetting thread_id**: All turns appear as unrelated traces
- **Shared thread_id across users**: Different users' conversations get mixed
- **Thread_id per turn instead of per session**: Each turn becomes its own thread
- **Not evaluating threads**: Missing conversation-level quality issues
