import { formatCost, formatDuration, formatKpi, formatRate } from "../lib/format";
import { StatusBadge, Tag } from "./ui";

export interface Kpi {
  name: string;
  grader: string;
  aggregate: string;
  value?: number | null;
  samples: number;
  target?: string | null;
  status: string;
}

/** An agent's KPIs against their targets. */
export function KpiTable({ kpis, minRuns }: { kpis: readonly Kpi[]; minRuns: number }) {
  if (kpis.length === 0) {
    return <p className="muted">This agent has no KPIs. Add some to its spec to have it judged.</p>;
  }
  return (
    <table className="table">
      <thead>
        <tr>
          <th>KPI</th>
          <th className="num">Value</th>
          <th className="num">Target</th>
          <th className="num">Runs counted</th>
          <th>Standing</th>
        </tr>
      </thead>
      <tbody>
        {kpis.map((kpi) => (
          <tr key={kpi.name}>
            <td>
              <span className="strong">{kpi.name}</span>
              <span className="muted">
                {" "}
                {kpi.aggregate} of {kpi.grader}
              </span>
            </td>
            <td className="num">{formatKpi(kpi.value, kpi.grader)}</td>
            <td className="num">{kpi.target ?? "–"}</td>
            <td className="num">
              {kpi.samples}
              {kpi.status === "insufficient_data" && <span className="muted"> of {minRuns} needed</span>}
            </td>
            <td>
              <StatusBadge status={kpi.status} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export interface ModelScorecard {
  provider: string;
  model: string;
  shadow: boolean;
  runs: number;
  succeeded: number;
  avgCostUsd: number;
  avgSeconds: number;
  scorecard: { kpis: readonly Kpi[] };
}

/**
 * The same KPIs side by side for every model the agent has run on. Shadow
 * models replayed the primary's inputs with side effects simulated, so the
 * rows are a like-for-like comparison.
 */
export function ComparisonTable({ models }: { models: readonly ModelScorecard[] }) {
  const first = models[0];
  if (!first) {
    return <p className="muted">No finished runs in the current review window.</p>;
  }
  const kpiNames = first.scorecard.kpis.map((k) => k.name);
  return (
    <div className="table-scroll">
      <table className="table">
        <thead>
          <tr>
            <th>Model</th>
            <th className="num">Runs</th>
            <th className="num">Succeeded</th>
            <th className="num">Avg cost</th>
            <th className="num">Avg time</th>
            {kpiNames.map((name) => (
              <th key={name} className="num">
                {name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {models.map((m) => (
            <tr key={`${m.provider}/${m.model}/${m.shadow}`}>
              <td>
                <span className="strong">{m.model}</span> <span className="muted">{m.provider}</span>{" "}
                <Tag>{m.shadow ? "shadow" : "primary"}</Tag>
              </td>
              <td className="num">{m.runs}</td>
              <td className="num">{formatRate(m.succeeded, m.runs)}</td>
              <td className="num">{formatCost(m.avgCostUsd)}</td>
              <td className="num">{formatDuration(m.avgSeconds * 1000)}</td>
              {kpiNames.map((name) => {
                const kpi = m.scorecard.kpis.find((k) => k.name === name);
                return (
                  <td key={name} className="num">
                    {kpi ? formatKpi(kpi.value, kpi.grader) : "–"}
                    {kpi && kpi.status === "breach" && (
                      <span className="cell-flag" title="Misses the target">
                        {" "}
                        ✕
                      </span>
                    )}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
