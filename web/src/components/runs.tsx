import { Link } from "react-router-dom";
import { formatAgo, formatCost, formatCount, formatDuration } from "../lib/format";
import { JsonView, StatusBadge, Tag } from "./ui";

export interface RunRow {
  id: string;
  status: string;
  trigger: string;
  model: string;
  costUsd: number;
  activeMs: number;
  startedAt: string;
  error: string;
  agent?: { name: string } | null;
}

/** A list of runs, newest first, each linking to its trace. */
export function RunsTable({ runs, showAgent = false }: { runs: readonly RunRow[]; showAgent?: boolean }) {
  if (runs.length === 0) {
    return <p className="muted">No runs yet.</p>;
  }
  return (
    <div className="table-scroll">
      <table className="table">
        <thead>
          <tr>
            <th>Run</th>
            {showAgent && <th>Agent</th>}
            <th>Status</th>
            <th>Started by</th>
            <th className="num">Cost</th>
            <th className="num">Time</th>
            <th className="num">Started</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id}>
              <td>
                <Link to={`/runs/${run.id}`} className="mono">
                  {run.id.slice(-8)}
                </Link>
              </td>
              {showAgent && <td>{run.agent && <Link to={`/agents/${run.agent.name}`}>{run.agent.name}</Link>}</td>}
              <td>
                <StatusBadge status={run.status} />
                {run.error && <div className="muted small clip">{run.error}</div>}
              </td>
              <td>{run.trigger}</td>
              <td className="num">{formatCost(run.costUsd)}</td>
              <td className="num">{formatDuration(run.activeMs)}</td>
              <td className="num">{formatAgo(run.startedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export interface Step {
  seq: number;
  kind: string;
  name: string;
  input?: unknown;
  output?: unknown;
  isError: boolean;
  simulated: boolean;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  costUsd: number;
  latencyMs: number;
}

/** A run's trace: every model call and tool call in order. */
export function Trace({ steps }: { steps: readonly Step[] }) {
  if (steps.length === 0) {
    return <p className="muted">Nothing recorded yet.</p>;
  }
  return (
    <ol className="trace">
      {steps.map((step) => (
        <li key={step.seq} className="trace-step">
          <details>
            <summary>
              <span className="trace-kind">{step.kind === "llm" ? "Model" : "Tool"}</span>
              <span className="strong">{step.name}</span>
              {step.isError && <Tag tone="critical">✕ error</Tag>}
              {step.simulated && <Tag tone="warning">simulated</Tag>}
              <span className="trace-meta">
                {step.kind === "llm" && (
                  <>
                    {formatCount(step.inputTokens + step.cacheReadTokens)} in · {formatCount(step.outputTokens)} out ·{" "}
                    {formatCost(step.costUsd)} ·{" "}
                  </>
                )}
                {formatDuration(step.latencyMs)}
              </span>
            </summary>
            <div className="trace-body">
              <JsonView label={step.kind === "llm" ? "Sent since the last call" : "Input"} value={step.input} />
              <JsonView label={step.kind === "llm" ? "Reply" : "Result"} value={step.output} />
            </div>
          </details>
        </li>
      ))}
    </ol>
  );
}
