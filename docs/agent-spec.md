# Agent spec reference

An agent is one YAML document. Unknown fields are rejected, so a typo cannot
silently switch off a limit or a KPI. `roster agents validate FILE` reports
every problem in a file at once.

## Top level

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Lowercase letters, digits and dashes. Applying a spec with an existing name updates that agent. |
| `description` | no | One line shown in the dashboard. |
| `model` | yes | The model the agent runs on. See [Model](#model). |
| `instructions` | yes | The system prompt. |
| `prompt` | no | A Go `text/template` rendered with the run's input to make the first user message. A missing key is an error. Without it, the input is shown to the model as JSON. |
| `triggers` | no | What starts runs. See [Triggers](#triggers). |
| `tools` | no | What the agent can call. See [Tools](#tools). |
| `mcp_servers` | no | MCP servers to take tools from. See [MCP servers](#mcp-servers). |
| `output` | no | The structured result the agent must submit. See [Output](#output). |
| `limits` | no | Bounds on a single run. See [Limits](#limits). |
| `kpis` | no | The numbers the agent is judged on. See [KPIs](#kpis). |
| `review` | no | What happens when a KPI misses its target. See [Review](#review). |
| `shadow` | no | Other models to replay runs on. See [Shadow models](#shadow-models). |

## Model

```yaml
model:
  provider: anthropic        # anthropic, openai, xai or ollama
  name: claude-opus-5-5
  effort: low                # reasoning effort, for models that take one
  max_tokens: 16000          # cap on one response; default depends on the provider
  temperature: 0.2           # sent only when set; several current models reject it
  fallbacks: default         # anthropic only; see below
```

`fallbacks` asks the Claude API to retry a request on another model when a
safety classifier declines it, instead of failing the run. `default` uses the
routing Anthropic recommends; a list of model names picks the substitutes
yourself. The trace records which model actually answered, and the run is
costed at that model's price.

## Triggers

Each trigger sets exactly one of `event`, `schedule` or `webhook`.

```yaml
triggers:
  - event: ticket.created      # the emitEvent mutation, or a Pub/Sub message
  - schedule: every 15m        # or a five-field cron expression: "0 8 * * *"
    input: {scope: day}        # the fixed input of scheduled runs
  - webhook: true              # POST /hooks/<agent name>, body is the input
```

An `every` schedule fires at multiples of its interval on the clock, so every
instance agrees on the firing times. A schedule that came due while nothing
was running fires once on the next tick, however many times it was missed,
provided the latest missed time is less than 24 hours old.

## Tools

A tool sets exactly one of `graphql` or `builtin`.

```yaml
tools:
  - name: get_order
    description: Fetch an order with the customer who placed it.
    input_schema:                       # JSON Schema; every call is validated
      type: object
      properties:
        id: {type: string}
      required: [id]
      additionalProperties: false
    graphql:
      endpoint: ${SHOP_GRAPHQL_URL}
      headers: {Authorization: "Bearer ${SHOP_API_TOKEN}"}
      query: |
        query($id: ID!) { order(id: $id) { id status } }

  - name: refund_order
    description: Refund an order in full.
    requires_approval: true             # a person approves each call
    graphql: { ... }

  - name: lookup
    builtin: lookup                     # a tool registered in Go
```

- **`graphql`** runs the operation with the tool's input as its variables and
  returns the response's `data`. GraphQL errors and HTTP failures come back to
  the model as an error result, so it can correct itself.
- **`builtin`** names a tool written in Go. The `roster` binary registers
  none; builtins are for programs that embed Roster and pass their own tools
  to the tool builder.
- **`${NAME}`** in an endpoint or header is read from the environment when the
  agent runs. An unset variable fails the run rather than sending a request
  with an empty credential. Secrets never appear in the stored spec.
- **`side_effect: true`** marks a tool that changes the outside world. Shadow
  runs do not execute it; the model is told the action was simulated.
- **`requires_approval: true`** suspends the run until a person decides. It
  implies `side_effect`. A rejected call is reported to the model with the
  reviewer's reason.

A call with input that fails the schema, or to a tool that does not exist, is
answered with an error the model can read. It does not fail the run.

## MCP servers

```yaml
mcp_servers:
  - name: warehouse
    url: https://wms.internal/mcp          # streamable HTTP; or
    # command: [wms-mcp, --stdio]          # a local process over stdio
    headers: {Authorization: "Bearer ${WMS_TOKEN}"}
    tools:
      - name: stock_level
      - name: move_stock
        requires_approval: true
```

Only the tools listed are offered to the model. Naming a tool the server does
not have is an error. Descriptions and input schemas come from the server.

## Output

```yaml
output:
  schema:
    type: object
    properties:
      flag: {type: boolean}
    required: [flag]
```

With an output schema, the agent gets a `submit_result` tool and the run
succeeds when it submits a result that fits. A result that does not fit is
sent back with the reason. An agent that stops without submitting is reminded
once, then failed. Without an output schema, the run's output is the model's
final text.

Graders read the submitted result, which is why a shadow run can be graded
even though its side effects never happened.

## Limits

| Field | Default | Meaning |
| --- | --- | --- |
| `max_steps` | 12 | Model calls per run. |
| `max_cost_usd` | none | The run stops once its model spend passes this. |
| `timeout` | 5m | Time spent executing. Waiting for approval does not count. |
| `tool_timeout` | 30s | Time allowed for one tool call. |

## KPIs

```yaml
kpis:
  - name: right_call               # shown in the dashboard and the audit trail
    grader: expected
    params: {fields: [action]}
    target: ">= 0.95"              # >=, <=, >, < or ==; omit to track without judging
  - name: p95_seconds
    grader: latency
    aggregate: p95                 # mean (default), sum, min, max, p50, p95
```

| Grader | Metrics (first is the default) | Params |
| --- | --- | --- |
| `expected` | `match`: 1 when every compared field equals the ground truth. `field_accuracy`: the share that do. | `fields` (default: all fields in the ground truth), `tolerance` (relative, for numbers) |
| `classification` | `correct`. `precision`: scored only when the agent said yes. `recall`: scored only when the truth was yes. | `field`, `positive` (default `true`) |
| `judge` | `score`, 1 to 5. Failed runs are not judged. | `rubric`, `model: {provider, name, effort}` |
| `outcome` | `success`: 1 when the run did not fail. | |
| `cost` | `usd` | |
| `latency` | `seconds` | |
| `steps` | `llm_calls`, `tool_calls` | |
| `approvals` | `approved`: the share of the run's approval requests that were approved. Absent when it asked for none. | |

Strings compare without regard to case or surrounding space. A failed run
scores 0 on `expected` and counts as a "no" for `classification`: it was asked
something that had an answer and did not give one.

The mean of `precision` over a window is true positives over all positives
called, and the mean of `recall` is true positives over all actual positives,
because each is only scored on the runs where it applies.

## Review

```yaml
review:
  window: 100          # the most recent graded runs that count (default 50)
  min_runs: 20         # samples a KPI needs before it can miss (default 20)
  on_breach: pause     # or alert (default pause)
```

`pause` stops the agent and records why. `alert` records the breach in the
agent's history once and lets the agent carry on; use it for agents whose
absence would hurt more than their mistakes.

## Shadow models

```yaml
shadow:
  - provider: ollama
    name: llama3.2
    sample_rate: 0.25    # the share of runs to replay (default 1)
```

Each sampled run is replayed on the shadow model with the same input and
ground truth. Tools without side effects run for real; tools with side effects
are simulated; nothing waits for approval. Shadow runs are graded with the
agent's own KPIs and appear beside the primary model in the comparison. They
never affect whether the agent is paused.
