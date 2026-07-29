import { useState, type FormEvent } from "react";
import { NavLink, Route, Routes } from "react-router-dom";
import { useQuery } from "urql";
import { credentials } from "./api/client";
import { graphql } from "./gql";
import { usePolling } from "./lib/usePolling";
import { AgentPage } from "./pages/AgentPage";
import { ApprovalsPage } from "./pages/ApprovalsPage";
import { FleetPage } from "./pages/FleetPage";
import { RunPage } from "./pages/RunPage";

const PendingCount = graphql(`
  query PendingCount {
    fleet {
      pendingApprovals
    }
  }
`);

export function App() {
  return (
    <>
      <header className="topbar">
        <NavLink to="/" className="brand">
          Roster
        </NavLink>
        <nav>
          <NavLink to="/" end>
            Fleet
          </NavLink>
          <ApprovalsLink />
        </nav>
        <Settings />
      </header>
      <main>
        <Routes>
          <Route path="/" element={<FleetPage />} />
          <Route path="/agents/:name" element={<AgentPage />} />
          <Route path="/runs/:id" element={<RunPage />} />
          <Route path="/approvals" element={<ApprovalsPage />} />
          <Route path="*" element={<p className="notice">There is nothing at this address.</p>} />
        </Routes>
      </main>
    </>
  );
}

/** The inbox link carries its count so nobody has to open it to find out. */
function ApprovalsLink() {
  const [{ data }, refresh] = useQuery({ query: PendingCount });
  usePolling(() => refresh({ requestPolicy: "network-only" }));
  const pending = data?.fleet.pendingApprovals ?? 0;
  return (
    <NavLink to="/approvals">
      Approvals
      {pending > 0 && (
        <span className="count" aria-label={`${pending} waiting`}>
          {pending}
        </span>
      )}
    </NavLink>
  );
}

/** Where a person enters the API token and the name recorded against their actions. */
function Settings() {
  const [token, setToken] = useState(credentials.token);
  const [user, setUser] = useState(credentials.user);
  const [saved, setSaved] = useState(false);

  const save = (e: FormEvent) => {
    e.preventDefault();
    credentials.save(token, user);
    setSaved(true);
  };

  return (
    <details className="settings">
      <summary>Settings</summary>
      <form onSubmit={save} className="settings-panel">
        <label>
          Your name
          <input
            value={user}
            onChange={(e) => {
              setUser(e.target.value);
              setSaved(false);
            }}
            placeholder="Shown in the audit trail"
          />
        </label>
        <label>
          API token
          <input
            type="password"
            value={token}
            onChange={(e) => {
              setToken(e.target.value);
              setSaved(false);
            }}
            placeholder="Leave empty if the API is open"
            autoComplete="off"
          />
        </label>
        <button type="submit" className="primary">
          Save
        </button>
        {saved && <span className="muted small"> Saved. It applies from the next refresh.</span>}
      </form>
    </details>
  );
}
