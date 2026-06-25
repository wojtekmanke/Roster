import { describe, expect, it } from "vitest";
import {
  formatAgo,
  formatCost,
  formatCount,
  formatDuration,
  formatKpi,
  formatRate,
  statusMeta,
  summariseKpis,
} from "./format";

describe("formatCost", () => {
  it.each([
    [0, "$0"],
    [0.00042, "$0.0004"],
    [0.0099, "$0.0099"],
    [0.01, "$0.01"],
    [1.234, "$1.23"],
    [99.999, "$100.00"],
    [1234.5, "$1,235"],
  ])("formats %s as %s", (usd, want) => {
    expect(formatCost(usd)).toBe(want);
  });
});

describe("formatDuration", () => {
  it.each([
    [0, "0ms"],
    [850, "850ms"],
    [1000, "1.0s"],
    [12_345, "12.3s"],
    [60_000, "1m 0s"],
    [125_000, "2m 5s"],
  ])("formats %sms as %s", (ms, want) => {
    expect(formatDuration(ms)).toBe(want);
  });
});

describe("formatAgo", () => {
  const now = new Date("2026-10-12T12:00:00Z");
  it.each([
    [null, "never"],
    [undefined, "never"],
    ["2026-10-12T11:59:58Z", "just now"],
    ["2026-10-12T11:59:30Z", "30s ago"],
    ["2026-10-12T11:45:00Z", "15m ago"],
    ["2026-10-12T07:00:00Z", "5h ago"],
    ["2026-10-09T12:00:00Z", "3d ago"],
    // A clock slightly ahead of the server must not print a negative age.
    ["2026-10-12T12:00:03Z", "just now"],
  ])("formats %s as %s", (iso, want) => {
    expect(formatAgo(iso, now)).toBe(want);
  });
});

describe("formatRate and formatCount", () => {
  it("shows a dash when there is nothing to divide by", () => {
    expect(formatRate(0, 0)).toBe("–");
  });
  it("rounds to whole percent", () => {
    expect(formatRate(2, 3)).toBe("67%");
    expect(formatRate(5, 5)).toBe("100%");
  });
  it("groups thousands", () => {
    expect(formatCount(12345)).toBe("12,345");
  });
});

describe("formatKpi", () => {
  it("uses the grader to pick the unit", () => {
    expect(formatKpi(0.0123, "cost")).toBe("$0.01");
    expect(formatKpi(2.5, "latency")).toBe("2.5s");
    expect(formatKpi(0.66667, "expected")).toBe("0.667");
    expect(formatKpi(4, "judge")).toBe("4");
  });
  it("shows a dash for a KPI with no samples", () => {
    expect(formatKpi(null, "expected")).toBe("–");
    expect(formatKpi(undefined, "cost")).toBe("–");
  });
  it("keeps a real zero", () => {
    expect(formatKpi(0, "expected")).toBe("0");
  });
});

describe("statusMeta", () => {
  it("gives every known status a label, a tone and an icon", () => {
    for (const status of ["active", "paused", "failed", "breach", "ok", "pending", "awaiting_approval"]) {
      const meta = statusMeta(status);
      expect(meta.label).not.toBe(status);
      expect(meta.icon).not.toBe("");
    }
  });
  it("passes unknown statuses through rather than hiding them", () => {
    expect(statusMeta("quarantined")).toEqual({ label: "quarantined", tone: "neutral", icon: "–" });
  });
});

describe("summariseKpis", () => {
  it("names the missed targets first", () => {
    expect(
      summariseKpis([
        { name: "accuracy", status: "breach" },
        { name: "cost", status: "ok" },
        { name: "recall", status: "breach" },
      ]),
    ).toEqual({ text: "Missing accuracy, recall", tone: "critical" });
  });
  it("counts met targets and ignores untargeted KPIs", () => {
    expect(
      summariseKpis([
        { name: "accuracy", status: "ok" },
        { name: "cost", status: "ok" },
        { name: "p95", status: "no_target" },
      ]),
    ).toEqual({ text: "2 of 2 targets met", tone: "good" });
  });
  it("stays neutral while some targets lack data", () => {
    expect(
      summariseKpis([
        { name: "accuracy", status: "ok" },
        { name: "precision", status: "insufficient_data" },
      ]),
    ).toEqual({ text: "1 of 2 targets met", tone: "neutral" });
  });
  it("says so when nothing can be judged yet", () => {
    expect(summariseKpis([{ name: "accuracy", status: "insufficient_data" }])).toEqual({
      text: "Too few runs to judge",
      tone: "neutral",
    });
    expect(summariseKpis([{ name: "p95", status: "no_target" }])).toEqual({ text: "No targets set", tone: "neutral" });
    expect(summariseKpis([])).toEqual({ text: "No targets set", tone: "neutral" });
  });
});
