import { useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";
import { useMutation, useQuery } from "urql";
import { RunsTable } from "../components/runs";
import { ComparisonTable, KpiTable } from "../components/scorecard";
import { PageState, Section, StatusBadge, Tag } from "../components/ui";
import { graphql } from "../gql";
import { formatAgo } from "../lib/format";
import { usePolling } from "../lib/usePolling";

const AgentQuery = graphql(`
  query Agent($name: String!) {
    agent(name: $name) {
      id
      name
      description
      status
      statusReason
      provider
      model
      specYaml
      reviewSince
      triggers
      tools {
        name
        description
        kind
        sideEffect
        requiresApproval
      }
      review {
        window
        minRuns
        onBreach
      }
      scorecard {
        runs
        kpis {
          name
          grader
          aggregate
          value
          samples
          target
          status
        }
      }
      comparison {
        provider
        model
        shadow
        runs
        succeeded
        avgCostUsd
        avgSeconds
        scorecard {
          kpis {
            name
            grader
            aggregate
            value
            samples
            target
            status
          }
        }
      }
      runs(limit: 20) {
        id
        status
        trigger
        model
        costUsd
        activeMs
        startedAt
        error
      }
      events(limit: 15) {
        id
        kind
        actor
        reason
        createdAt
      }
    }
  }
`);

const PauseAgent = graphql(`
  mutation PauseAgent($name: String!, $reason: String!) {
    pauseAgent(name: $name, reason: $reason) {
      id
      status
      statusReason
    }
  }
`);

const ResumeAgent = graphql(`
  mutation ResumeAgent($name: String!, $reason: String) {
    resumeAgent(name: $name, reason: $reason) {
      id
      status
      statusReason
    }
  }
`);

const TriggerAgent = graphql(`
  mutation TriggerAgent($name: String!, $input: JSON) {
    triggerAgent(name: $name, input: $input)
  }
`);

/** One agent: its standing against its targets, its models compared, and its history. */
export function AgentPage() {
  const { name = "" } = useParams();
  const [{ data, fetching, error }, refresh] = useQuery({ query: AgentQuery, variables: { name } });
  usePolling(() => refresh({ requestPolicy: "network-only" }));

  if (!data) return <PageState fetching={fetching} error={error?.message} />;
  const agent = data.agent;
  if (!agent) return <PageState fetching={false} missing={`There is no agent named "${name}".`} />;

  return (
    <>
      <header className="page-head">
        <div>
          <h1>
            {agent.name} <StatusBadge status={agent.status} />
          </h1>
          <p className="muted">{agent.description}</p>
          <p className="small">
            <span className="strong">{agent.model}</span> <span className="muted">via {agent.provider}</span>
            {agent.triggers.map((t) => (
              <Tag key={t}>{t}</Tag>
            ))}
          </p>
        </div>
        <AgentActions name={agent.name} status={agent.status} onDone={() => refresh({ requestPolicy: "network-only" })} />
      </header>

      {agent.status !== "active" && agent.statusReason && (
        <div className="notice notice-warning">
          <strong>{agent.status === "paused" ? "Paused" : "Retired"}:</strong> {agent.statusReason}
        </div>
      )}

      <Section
        title="Scorecard"
        aside={
          <span className="muted small">
            Last {agent.review.window} runs since {formatAgo(agent.reviewSince)} · {agent.scorecard.runs} graded · a
            missed target will {agent.review.onBreach === "pause" ? "pause the agent" : "raise an alert"}
          </span>
        }
      >
        <KpiTable kpis={agent.scorecard.kpis} minRuns={agent.review.minRuns} />
      </Section>

      <Section
        title="Models compared"
        aside={<span className="muted small">Shadow models replay the same inputs with side effects simulated</span>}
      >
        <ComparisonTable models={agent.comparison} />
      </Section>

      <Section title="Recent runs">
        <RunsTable runs={agent.runs} />
      </Section>

      <Section title="Tools">
        {agent.tools.length === 0 ? (
          <p className="muted">This agent has no tools.</p>
        ) : (
          <table className="table">
            <tbody>
              {agent.tools.map((tool) => (
                <tr key={tool.name}>
                  <td className="mono">{tool.name}</td>
                  <td>{tool.description}</td>
                  <td>
                    <Tag>{tool.kind}</Tag>
                    {tool.requiresApproval ? (
                      <Tag tone="warning">needs approval</Tag>
                    ) : (
                      tool.sideEffect && <Tag>side effect</Tag>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="History">
        <table className="table">
          <tbody>
            {agent.events.map((event) => (
              <tr key={event.id}>
                <td className="strong">{event.kind.replace("_", " ")}</td>
                <td>{event.reason}</td>
                <td className="muted">by {event.actor}</td>
                <td className="num">{formatAgo(event.createdAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      <Section title="Spec">
        <details>
          <summary>Show the YAML this agent runs from</summary>
          <pre className="json-body">{agent.specYaml}</pre>
        </details>
      </Section>
    </>
  );
}

function AgentActions({ name, status, onDone }: { name: string; status: string; onDone: () => void }) {
  const [, pause] = useMutation(PauseAgent);
  const [, resume] = useMutation(ResumeAgent);
  const [, trigger] = useMutation(TriggerAgent);
  const [mode, setMode] = useState<"idle" | "pause" | "resume" | "trigger">("idle");
  const [text, setText] = useState("");
  const [problem, setProblem] = useState("");

  if (status === "retired") return null;

  const open = (next: typeof mode) => {
    setMode(next);
    setText("");
    setProblem("");
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    let failure: string | undefined;
    if (mode === "pause") {
      failure = (await pause({ name, reason: text })).error?.message;
    } else if (mode === "resume") {
      failure = (await resume({ name, reason: text })).error?.message;
    } else if (mode === "trigger") {
      let input: unknown = null;
      if (text.trim() !== "") {
        try {
          input = JSON.parse(text);
        } catch {
          setProblem("The input must be valid JSON.");
          return;
        }
      }
      failure = (await trigger({ name, input })).error?.message;
    }
    if (failure) {
      setProblem(failure);
      return;
    }
    setMode("idle");
    onDone();
  };

  if (mode === "idle") {
    return (
      <div className="actions">
        {status === "active" && (
          <>
            <button type="button" onClick={() => open("trigger")}>
              Run now
            </button>
            <button type="button" onClick={() => open("pause")}>
              Pause
            </button>
          </>
        )}
        {status === "paused" && (
          <button type="button" className="primary" onClick={() => open("resume")}>
            Resume
          </button>
        )}
      </div>
    );
  }

  const labels = {
    pause: { field: "Why are you pausing it?", button: "Pause agent", required: true },
    resume: { field: "What changed? (optional)", button: "Resume agent", required: false },
    trigger: { field: "Input as JSON (optional)", button: "Queue run", required: false },
  }[mode];

  return (
    <form className="action-form" onSubmit={submit}>
      <label>
        {labels.field}
        <textarea value={text} onChange={(e) => setText(e.target.value)} required={labels.required} rows={2} autoFocus />
      </label>
      {mode === "resume" && (
        <p className="muted small">Resuming restarts the review window, so earlier runs no longer count.</p>
      )}
      {problem && (
        <p className="problem" role="alert">
          {problem}
        </p>
      )}
      <div className="actions">
        <button type="submit" className="primary">
          {labels.button}
        </button>
        <button type="button" onClick={() => setMode("idle")}>
          Cancel
        </button>
      </div>
    </form>
  );
}
