import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation } from "urql";
import { graphql } from "../gql";
import { formatAgo } from "../lib/format";
import { JsonView, StatusBadge } from "./ui";

const DecideApproval = graphql(`
  mutation DecideApproval($id: ID!, $approve: Boolean!, $reason: String) {
    decideApproval(id: $id, approve: $approve, reason: $reason) {
      id
      status
    }
  }
`);

export interface ApprovalItem {
  id: string;
  tool: string;
  input?: unknown;
  status: string;
  createdAt: string;
  decidedBy?: string;
  reason?: string;
  agent?: { name: string } | null;
  run?: { id: string } | null;
}

/**
 * One tool call waiting for a person. The exact arguments are shown because
 * that is what is being approved, not a summary of them.
 */
export function ApprovalCard({ approval, onDecided }: { approval: ApprovalItem; onDecided: () => void }) {
  const [{ fetching }, decide] = useMutation(DecideApproval);
  const [reason, setReason] = useState("");
  const [problem, setProblem] = useState("");

  const act = async (approve: boolean) => {
    if (!approve && reason.trim() === "") {
      setProblem("Say why, so the agent and the next reviewer know.");
      return;
    }
    const result = await decide({ id: approval.id, approve, reason: reason.trim() || null });
    if (result.error) {
      setProblem(result.error.message);
      return;
    }
    onDecided();
  };

  return (
    <article className="card">
      <header className="card-head">
        <div>
          <span className="mono strong">{approval.tool}</span>
          {approval.agent && (
            <>
              {" "}
              <span className="muted">requested by</span>{" "}
              <Link to={`/agents/${approval.agent.name}`}>{approval.agent.name}</Link>
            </>
          )}
        </div>
        <div className="muted small">
          {approval.run && <Link to={`/runs/${approval.run.id}`}>See the run</Link>} · {formatAgo(approval.createdAt)}
        </div>
      </header>
      <JsonView value={approval.input} />

      {approval.status === "pending" ? (
        <>
          <label className="stack">
            <span className="small muted">Note (required to reject)</span>
            <input value={reason} onChange={(e) => setReason(e.target.value)} />
          </label>
          {problem && (
            <p className="problem" role="alert">
              {problem}
            </p>
          )}
          <div className="actions">
            <button type="button" className="primary" disabled={fetching} onClick={() => act(true)}>
              Approve
            </button>
            <button type="button" disabled={fetching} onClick={() => act(false)}>
              Reject
            </button>
          </div>
        </>
      ) : (
        <p className="small">
          <StatusBadge status={approval.status} />{" "}
          <span className="muted">
            by {approval.decidedBy || "unknown"}
            {approval.reason ? `: ${approval.reason}` : ""}
          </span>
        </p>
      )}
    </article>
  );
}
