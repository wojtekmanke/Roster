// Formatting helpers shared by the dashboard's tables and tiles.

/** Dollars with enough precision for per-run costs, which are often under a cent. */
export function formatCost(usd: number): string {
  if (usd === 0) return "$0";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  if (usd < 100) return `$${usd.toFixed(2)}`;
  return `$${Math.round(usd).toLocaleString("en-US")}`;
}

/** A duration given in milliseconds, in the largest sensible unit. */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${Math.round(seconds - minutes * 60)}s`;
}

/** "3 minutes ago" for a timestamp; "never" for a missing one. */
export function formatAgo(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return "never";
  const seconds = Math.max(0, Math.round((now.getTime() - new Date(iso).getTime()) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

/** Whole numbers with thousands separators. */
export function formatCount(n: number): string {
  return n.toLocaleString("en-US");
}

/** A share of a total as a percentage, or a dash when there is no total. */
export function formatRate(part: number, total: number): string {
  if (total === 0) return "–";
  return `${Math.round((part / total) * 100)}%`;
}

/**
 * A KPI value. Graders that produce money or time say so in their name, so
 * the grader picks the unit; everything else is a plain number trimmed to
 * three decimals.
 */
export function formatKpi(value: number | null | undefined, grader: string): string {
  if (value === null || value === undefined) return "–";
  if (grader === "cost") return formatCost(value);
  if (grader === "latency") return formatDuration(value * 1000);
  return String(Math.round(value * 1000) / 1000);
}

export type Tone = "good" | "warning" | "critical" | "neutral";

export interface StatusMeta {
  label: string;
  tone: Tone;
  /** A glyph shown beside the label, so status never rests on colour alone. */
  icon: string;
}

const check = "✓";
const cross = "✕";
const pause = "⏸";
const dots = "…";
const dash = "–";

const statuses: Record<string, StatusMeta> = {
  // Agents
  active: { label: "Active", tone: "good", icon: check },
  paused: { label: "Paused", tone: "warning", icon: pause },
  retired: { label: "Retired", tone: "neutral", icon: dash },
  // Runs
  running: { label: "Running", tone: "neutral", icon: dots },
  awaiting_approval: { label: "Awaiting approval", tone: "warning", icon: pause },
  succeeded: { label: "Succeeded", tone: "good", icon: check },
  failed: { label: "Failed", tone: "critical", icon: cross },
  // KPIs
  ok: { label: "On target", tone: "good", icon: check },
  breach: { label: "Missed", tone: "critical", icon: cross },
  insufficient_data: { label: "Too few runs", tone: "neutral", icon: dots },
  no_target: { label: "Tracked", tone: "neutral", icon: dash },
  // Approvals
  pending: { label: "Pending", tone: "warning", icon: pause },
  approved: { label: "Approved", tone: "good", icon: check },
  rejected: { label: "Rejected", tone: "critical", icon: cross },
};

/** How to show a status string from the API. Unknown values pass through as neutral. */
export function statusMeta(status: string): StatusMeta {
  return statuses[status] ?? { label: status, tone: "neutral", icon: dash };
}

interface KpiLike {
  name: string;
  status: string;
}

/** One line on how an agent stands against its targets, for the fleet table. */
export function summariseKpis(kpis: readonly KpiLike[]): { text: string; tone: Tone } {
  const targeted = kpis.filter((k) => k.status !== "no_target");
  if (targeted.length === 0) return { text: "No targets set", tone: "neutral" };
  const missed = targeted.filter((k) => k.status === "breach");
  if (missed.length > 0) {
    return { text: `Missing ${missed.map((k) => k.name).join(", ")}`, tone: "critical" };
  }
  const met = targeted.filter((k) => k.status === "ok").length;
  if (met === 0) return { text: "Too few runs to judge", tone: "neutral" };
  return { text: `${met} of ${targeted.length} targets met`, tone: met === targeted.length ? "good" : "neutral" };
}
