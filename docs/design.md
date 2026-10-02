# Design notes

The decisions that shaped Roster, and what each one costs.

## Agents are judged on outcomes, with ground truth kept away from them

Counting successful runs says little: an agent can finish every run and be
wrong every time. So a run can carry **ground truth**, the answer a correct
agent would give, which goes to the graders and never into the conversation.
In the demo it comes from the scenario generator, which knows the answers
because it built the data. In production it usually arrives late, as a
reopened ticket or a chargeback, which is why a run can be labelled after the
fact and graded again.

The cost: someone has to supply the truth. Where nobody can, the `judge`
grader scores output against a written rubric, and the run-fact graders (cost,
latency, approval rate) need nothing at all.

## Pausing is automatic, and biased towards not acting on thin evidence

A KPI cannot miss its target until it has `min_runs` samples. A metric that
does not apply to a run produces no sample: precision is only scored on runs
where the agent said yes, so an agent that rarely flags anything takes longer
to judge on precision, which is the right behaviour.

Resuming an agent restarts its review window. Otherwise the runs that got it
paused would pause it again on its first run back, before a fix could show.

The cost: a bad agent keeps working until it has `min_runs` graded runs. Lower
`min_runs` for agents whose mistakes are expensive.

## Shadow runs simulate side effects

To compare models fairly they must see the same inputs, but a second model
must not send a second reply to the customer. Tools declare whether they
change the outside world; in a shadow run those calls are answered with
"simulated" and everything else runs for real. Graders read the result the
agent submits rather than the state of the world, so a shadow run is graded
exactly like a primary one.

A shadow job takes its primary's place in the queue rather than the back of
it, so that under a backlog the two models are still being compared on the
same stretch of work.

The cost: a shadow never sees the real result of a side effect. For an agent
whose later steps depend on what a write returned, the shadow's path diverges
from that point.

## A run whose worker dies is failed, not restarted

A worker that crashes mid-run leaves a run that may already have refunded an
order. Restarting it could refund the order twice. So a redelivered job does
not re-execute: the run is marked failed as interrupted and graded as a
failure. Delivery of jobs is at-least-once; execution of side effects is
at-most-once.

The cost: an interrupted run's work is lost and has to be triggered again by
whoever cares. For agents made only of idempotent tools this is stricter than
necessary.

## The queue is a Postgres table

Jobs are rows claimed with `FOR UPDATE SKIP LOCKED`. Roster already needs
Postgres, so the queue adds no infrastructure, and enqueueing can share a
transaction with the change that caused it: deciding the last pending approval
of a run and queueing the run to continue either both happen or neither does.

The cost: throughput is bounded by the database. Agent runs take seconds and
cost money, so a fleet reaches other limits long before this one. Pub/Sub is
supported where it fits better, as a way for other systems to deliver events.

## Approval suspends the run instead of blocking a worker

A run that needs approval is written to the database as a conversation state
and its worker moves on. When the last approval is decided, a job resumes the
run from that state, possibly on another instance. Calls made in the same turn
that needed no approval have already run; their results are held and sent back
together with the approved ones, in the order the model asked, because
providers expect one message answering all of a turn's calls.

A run resumes on the model it started with, even if the spec has since changed
models: a conversation cannot switch models midway without losing the
provider's signed reasoning blocks.

## Providers sit behind one small interface

The agent loop speaks a neutral conversation format; each adapter translates
it. An assistant turn also keeps the provider's own encoding, which is replayed
verbatim when the conversation continues. Claude's thinking blocks are signed
and must come back unchanged, and nothing in a neutral format could carry them
faithfully.

OpenAI, Grok and Ollama share one adapter because they share a protocol; the
differences between them are three constructor arguments.

Token usage is normalised so "input tokens" excludes cached tokens for every
provider. Costs come from a price table rather than the providers' APIs,
because not every provider reports cost, and a table can be corrected without
a release.

## Tools are declared, and GraphQL is a first-class way to declare them

Most of what an agent needs already exists behind an internal API. Declaring a
tool as a GraphQL operation means connecting an agent to that API is a change
to a YAML file, reviewed like any other, rather than new Go code. The
operation is fixed in the spec; the model supplies only the variables, which
are validated against a schema. The model cannot compose its own queries.

The cost: an API that is not GraphQL needs an MCP server or a small Go tool.

## It can scale to zero

Cloud Run gives a container CPU only while it is handling a request, unless
you pay for an instance to stay up. So besides the usual in-process workers,
Roster exposes `/internal/tick` and `/internal/work`, and Cloud Scheduler calls
them every minute: the work happens inside the request. The schedule watermark
lives in the database, so firing times that fall while nothing is running are
caught up, once per trigger, on the next tick.

The cost: up to a minute of latency between an event and its run. Keep one
instance warm with workers on if that matters.

## What is deliberately missing

- **Multi-tenancy and per-user permissions.** One token, one fleet.
- **Streaming.** Runs are background work; nobody is watching tokens arrive.
- **Retries of failed runs.** The provider SDKs already retry transient errors
  within a call. A run that still fails is information the scorecard should
  see, not something to paper over.
- **Agent-to-agent orchestration.** Agents here are independent workers that
  react to events. One agent can cause an event another listens for, but
  Roster does not model conversations between them.
