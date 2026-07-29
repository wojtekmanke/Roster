import { Link } from "react-router-dom";
import { useQuery } from "urql";
import { PageState, Section, StatTile, StatusBadge } from "../components/ui";
import { graphql } from "../gql";
import { formatAgo, formatCost, formatCount, formatRate, summariseKpis } from "../lib/format";
import { usePolling } from "../lib/usePolling";

const FleetQuery = graphql(`
  query Fleet {
    fleet {
      active
      paused
      retired
      pendingApprovals
      queueDepth
      runs24h
      costUsd24h
    }
    agents {
      id
      name
      description
      status
      statusReason
      model
      stats {
        runs
        succeeded
        failed
        costUsd
        lastRunAt
      }
      scorecard {
        kpis {
          name
          status
        }
      }
    }
  }
`);

/** The whole fleet at a glance: who is working, who was stopped, and why. */
export function FleetPage() {
  const [{ data, fetching, error }, refresh] = useQuery({ query: FleetQuery });
  usePolling(() => refresh({ requestPolicy: "network-only" }));

  if (!data) return <PageState fetching={fetching} error={error?.message} />;
  const { fleet, agents } = data;

  return (
    <>
      <div className="tiles">
        <StatTile label="Agents working" value={formatCount(fleet.active)} />
        <StatTile
          label="Agents paused"
          value={formatCount(fleet.paused)}
          hint={fleet.retired > 0 ? `${fleet.retired} retired` : undefined}
        />
        <StatTile label="Runs, last 24h" value={formatCount(fleet.runs24h)} />
        <StatTile label="Spend, last 24h" value={formatCost(fleet.costUsd24h)} />
        <StatTile label="Waiting for approval" value={formatCount(fleet.pendingApprovals)} />
        <StatTile label="Queued" value={formatCount(fleet.queueDepth)} />
      </div>

      <Section title="Agents">
        {agents.length === 0 ? (
          <p className="muted">
            No agents yet. Apply a spec with <code>roster agents apply</code>.
          </p>
        ) : (
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>Agent</th>
                  <th>Status</th>
                  <th>Against targets</th>
                  <th className="num">Runs, 24h</th>
                  <th className="num">Succeeded</th>
                  <th className="num">Spend, 24h</th>
                  <th className="num">Last run</th>
                </tr>
              </thead>
              <tbody>
                {agents.map((agent) => {
                  const standing = summariseKpis(agent.scorecard.kpis);
                  return (
                    <tr key={agent.id}>
                      <td>
                        <Link to={`/agents/${agent.name}`} className="strong">
                          {agent.name}
                        </Link>
                        <div className="muted small">{agent.model}</div>
                      </td>
                      <td>
                        <StatusBadge status={agent.status} />
                        {agent.status !== "active" && agent.statusReason && (
                          <div className="muted small clip" title={agent.statusReason}>
                            {agent.statusReason}
                          </div>
                        )}
                      </td>
                      <td className={standing.tone === "critical" ? "strong" : undefined}>{standing.text}</td>
                      <td className="num">{formatCount(agent.stats.runs)}</td>
                      <td className="num">{formatRate(agent.stats.succeeded, agent.stats.succeeded + agent.stats.failed)}</td>
                      <td className="num">{formatCost(agent.stats.costUsd)}</td>
                      <td className="num">{formatAgo(agent.stats.lastRunAt)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Section>
    </>
  );
}
