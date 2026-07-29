import { screen, within } from "@testing-library/react";
import { CombinedError } from "urql";
import { describe, expect, it } from "vitest";
import { mockClient, renderWith } from "../test-utils";
import { FleetPage } from "./FleetPage";

const data = {
  fleet: { active: 3, paused: 1, retired: 0, pendingApprovals: 2, queueDepth: 14, runs24h: 1280, costUsd24h: 31.5 },
  agents: [
    {
      id: "agt_1", name: "support-triage", description: "", status: "active", statusReason: "", model: "claude-opus-5-5",
      stats: { runs: 900, succeeded: 880, failed: 20, costUsd: 22.4, lastRunAt: new Date().toISOString() },
      scorecard: { kpis: [{ name: "right_call", status: "ok" }, { name: "cost", status: "ok" }] },
    },
    {
      id: "agt_2", name: "fraud-screen", description: "", status: "paused", statusReason: "recall is 0.81 over 40 runs (target >= 0.95)", model: "claude-opus-5-5",
      stats: { runs: 0, succeeded: 0, failed: 0, costUsd: 0, lastRunAt: null },
      scorecard: { kpis: [{ name: "recall", status: "breach" }, { name: "precision", status: "ok" }] },
    },
  ],
};

describe("FleetPage", () => {
  it("leads with the fleet's headline numbers", () => {
    renderWith(<FleetPage />, mockClient({ data }).client);
    const tile = (label: string) => within(screen.getByText(label).closest(".tile") as HTMLElement);
    expect(tile("Agents working").getByText("3")).toBeInTheDocument();
    expect(tile("Agents paused").getByText("1")).toBeInTheDocument();
    expect(tile("Runs, last 24h").getByText("1,280")).toBeInTheDocument();
    expect(tile("Spend, last 24h").getByText("$31.50")).toBeInTheDocument();
    expect(tile("Waiting for approval").getByText("2")).toBeInTheDocument();
  });

  it("shows why a paused agent was paused, and which target it missed", () => {
    renderWith(<FleetPage />, mockClient({ data }).client);
    const row = within(screen.getByRole("link", { name: "fraud-screen" }).closest("tr")!);
    expect(row.getByText("Paused")).toBeInTheDocument();
    expect(row.getByText(/recall is 0.81 over 40 runs/)).toBeInTheDocument();
    expect(row.getByText("Missing recall")).toBeInTheDocument();
    // No runs means no success rate, not 0%.
    expect(row.getByText("–")).toBeInTheDocument();
    expect(row.getByText("never")).toBeInTheDocument();
  });

  it("summarises a healthy agent in one line", () => {
    renderWith(<FleetPage />, mockClient({ data }).client);
    const row = within(screen.getByRole("link", { name: "support-triage" }).closest("tr")!);
    expect(row.getByText("Active")).toBeInTheDocument();
    expect(row.getByText("2 of 2 targets met")).toBeInTheDocument();
    expect(row.getByText("98%")).toBeInTheDocument();
  });

  it("tells a new user how to add the first agent", () => {
    renderWith(<FleetPage />, mockClient({ data: { ...data, agents: [] } }).client);
    expect(screen.getByText(/No agents yet/)).toBeInTheDocument();
  });

  it("points at the token setting when the API refuses the request", () => {
    const error = new CombinedError({ networkError: new Error("Unauthorized") });
    renderWith(<FleetPage />, mockClient({ error }).client);
    expect(screen.getByRole("alert")).toHaveTextContent("Set the API token under Settings");
  });
});
