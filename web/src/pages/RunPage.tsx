import { Link, useParams } from "react-router-dom";
import { useQuery } from "urql";
import { ApprovalCard } from "../components/ApprovalCard";
import { Trace } from "../components/runs";
import { JsonView, PageState, Section, StatTile, StatusBadge, Tag } from "../components/ui";
import { graphql } from "../gql";
import { formatAgo, formatCost, formatCount, formatDuration, formatKpi } from "../lib/format";
import { usePolling } from "../lib/usePolling";

const RunQuery = graphql(`
  query Run($id: ID!) {
    run(id: $id) {
      id
      status
      trigger
      input
      expected
      output
      error
      provider
      model
      shadowOf
      inputTokens
      outputTokens
      cacheReadTokens
      costUsd
      llmCalls
      toolCalls
      activeMs
      startedAt
      agent {
        name
      }
      shadows {
        id
        status
        model
        costUsd
      }
      steps {
        seq
        kind
        name
        input
        output
        isError
        simulated
        inputTokens
        outputTokens
        cacheReadTokens
        costUsd
        latencyMs
      }
      approvals {
        id
        tool
        input
        status
        decidedBy
        reason
        createdAt
      }
      scores {
        kpi
        grader
        value
        detail
      }
    }
  }
`);

/** One run: what it was given, what it did step by step, and how it was graded. */
export function RunPage() {
  const { id = "" } = useParams();
  const [{ data, fetching, error }, refresh] = useQuery({ query: RunQuery, variables: { id } });
  const reload = () => refresh({ requestPolicy: "network-only" });
  usePolling(reload);

  if (!data) return <PageState fetching={fetching} error={error?.message} />;
  const run = data.run;
  if (!run) return <PageState fetching={false} missing={`There is no run with the ID "${id}".`} />;

  return (
    <>
      <header className="page-head">
        <div>
          <h1>
            Run <span className="mono">{run.id.slice(-8)}</span> <StatusBadge status={run.status} />
            {run.shadowOf && <Tag tone="warning">shadow</Tag>}
          </h1>
          <p className="small">
            <Link to={`/agents/${run.agent.name}`}>{run.agent.name}</Link> on <span className="strong">{run.model}</span>{" "}
            <span className="muted">
              via {run.provider} · started by {run.trigger} · {formatAgo(run.startedAt)}
            </span>
          </p>
          {run.shadowOf && (
            <p className="small muted">
              A replay of <Link to={`/runs/${run.shadowOf}`}>run {run.shadowOf.slice(-8)}</Link> on another model. Its side
              effects were simulated.
            </p>
          )}
        </div>
      </header>

      {run.error && (
        <div className="notice notice-critical" role="alert">
          <strong>Failed:</strong> {run.error}
        </div>
      )}

      <div className="tiles">
        <StatTile label="Cost" value={formatCost(run.costUsd)} />
        <StatTile label="Time working" value={formatDuration(run.activeMs)} hint="Excludes waiting for approval" />
        <StatTile label="Model calls" value={formatCount(run.llmCalls)} />
        <StatTile label="Tool calls" value={formatCount(run.toolCalls)} />
        <StatTile
          label="Tokens in / out"
          value={`${formatCount(run.inputTokens + run.cacheReadTokens)} / ${formatCount(run.outputTokens)}`}
          hint={run.cacheReadTokens > 0 ? `${formatCount(run.cacheReadTokens)} read from cache` : undefined}
        />
      </div>

      {run.approvals.length > 0 && (
        <Section title="Approvals">
          <div className="cards">
            {run.approvals.map((approval) => (
              <ApprovalCard key={approval.id} approval={approval} onDecided={reload} />
            ))}
          </div>
        </Section>
      )}

      <Section title="Result">
        <div className="columns">
          <JsonView label="Input" value={run.input} />
          <JsonView label="Output" value={run.output} />
          <JsonView label="Expected (hidden from the agent)" value={run.expected} />
        </div>
      </Section>

      <Section title="Grades">
        {run.scores.length === 0 ? (
          <p className="muted">
            {run.status === "succeeded" || run.status === "failed" ? "No KPI applied to this run." : "Graded when it finishes."}
          </p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>KPI</th>
                <th className="num">Value</th>
                <th>Why</th>
              </tr>
            </thead>
            <tbody>
              {run.scores.map((score) => (
                <tr key={score.kpi}>
                  <td>
                    <span className="strong">{score.kpi}</span> <span className="muted">{score.grader}</span>
                  </td>
                  <td className="num">{formatKpi(score.value, score.grader)}</td>
                  <td>{score.detail ? <JsonView value={score.detail} /> : <span className="muted">–</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      {run.shadows.length > 0 && (
        <Section title="Shadow runs">
          <table className="table">
            <tbody>
              {run.shadows.map((shadow) => (
                <tr key={shadow.id}>
                  <td>
                    <Link to={`/runs/${shadow.id}`} className="mono">
                      {shadow.id.slice(-8)}
                    </Link>
                  </td>
                  <td className="strong">{shadow.model}</td>
                  <td>
                    <StatusBadge status={shadow.status} />
                  </td>
                  <td className="num">{formatCost(shadow.costUsd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      <Section title="Trace">
        <Trace steps={run.steps} />
      </Section>
    </>
  );
}
