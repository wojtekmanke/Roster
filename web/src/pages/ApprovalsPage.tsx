import { useQuery } from "urql";
import { ApprovalCard } from "../components/ApprovalCard";
import { PageState, Section } from "../components/ui";
import { graphql } from "../gql";
import { usePolling } from "../lib/usePolling";

const ApprovalsQuery = graphql(`
  query Approvals {
    approvals {
      id
      tool
      input
      status
      createdAt
      run {
        id
      }
      agent {
        name
      }
    }
  }
`);

/** The inbox of tool calls waiting for a person, oldest first. */
export function ApprovalsPage() {
  const [{ data, fetching, error }, refresh] = useQuery({ query: ApprovalsQuery });
  const reload = () => refresh({ requestPolicy: "network-only" });
  usePolling(reload);

  if (!data) return <PageState fetching={fetching} error={error?.message} />;

  return (
    <Section title="Waiting for approval">
      {data.approvals.length === 0 ? (
        <p className="muted">Nothing is waiting. Agents ask here before taking actions marked as needing approval.</p>
      ) : (
        <div className="cards">
          {data.approvals.map((approval) => (
            <ApprovalCard key={approval.id} approval={approval} onDecided={reload} />
          ))}
        </div>
      )}
    </Section>
  );
}
