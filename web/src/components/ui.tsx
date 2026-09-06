import { useState, type ReactNode } from "react";
import { statusMeta, type Tone } from "../lib/format";

/** A status with its icon and label. Colour is a third cue, never the only one. */
export function StatusBadge({ status }: { status: string }) {
  const meta = statusMeta(status);
  return (
    <span className={`badge badge-${meta.tone}`}>
      <span className="badge-icon" aria-hidden="true">
        {meta.icon}
      </span>
      {meta.label}
    </span>
  );
}

/** A small marker for facts that are not statuses, such as "shadow". */
export function Tag({ children, tone = "neutral" }: { children: ReactNode; tone?: Tone }) {
  return <span className={`tag tag-${tone}`}>{children}</span>;
}

/** One headline number with its label. */
export function StatTile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
      {hint && <div className="tile-hint">{hint}</div>}
    </div>
  );
}

/** Pretty-printed JSON. Long values collapse behind a toggle. */
export function JsonView({ value, label }: { value: unknown; label?: string }) {
  const [open, setOpen] = useState(false);
  if (value === null || value === undefined) {
    return (
      <div className="json">
        {label && <div className="json-label">{label}</div>}
        <span className="muted">none</span>
      </div>
    );
  }
  const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  const long = text.split("\n").length > 12 || text.length > 600;
  return (
    <div className="json">
      {label && <div className="json-label">{label}</div>}
      <pre className={long && !open ? "json-body json-clipped" : "json-body"}>{text}</pre>
      {long && (
        <button type="button" className="link" onClick={() => setOpen(!open)}>
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}

/** A titled block of page content. */
export function Section({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className="section">
      <div className="section-head">
        <h2>{title}</h2>
        {aside}
      </div>
      {children}
    </section>
  );
}

/** What a page shows while loading, on error, or when its subject is missing. */
export function PageState({ fetching, error, missing }: { fetching: boolean; error?: string; missing?: string }) {
  if (error) {
    return (
      <div className="notice notice-critical" role="alert">
        <strong>Could not load.</strong> {error}
        {error.includes("401") || error.toLowerCase().includes("unauthorized") ? (
          <> Set the API token under Settings.</>
        ) : null}
      </div>
    );
  }
  if (missing) return <div className="notice">{missing}</div>;
  if (fetching) return <div className="muted">Loading…</div>;
  // Not loading, no error, no data: say so rather than leave a blank page.
  return <div className="muted">Nothing to show yet.</div>;
}
