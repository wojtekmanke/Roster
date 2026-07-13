import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWith } from "../test-utils";
import { RunsTable, Trace, type Step } from "./runs";

const base: Step = {
  seq: 0, kind: "llm", name: "claude-opus-5-5", input: { messages: [] }, output: { stop_reason: "tool_use" },
  isError: false, simulated: false, inputTokens: 1200, outputTokens: 80, cacheReadTokens: 300, costUsd: 0.0065, latencyMs: 1840,
};

describe("Trace", () => {
  it("lists model and tool steps with what they cost", () => {
    renderWith(
      <Trace
        steps={[
          base,
          { ...base, seq: 1, kind: "tool", name: "hold_order", simulated: true, latencyMs: 0, costUsd: 0 },
          { ...base, seq: 2, kind: "tool", name: "get_order", isError: true, latencyMs: 30_000 },
        ]}
      />,
    );
    const steps = screen.getAllByRole("listitem");
    expect(steps).toHaveLength(3);

    // Cached tokens are input too, and belong in the total a person reads.
    expect(within(steps[0]!).getByText(/1,500 in · 80 out · \$0\.0065/)).toBeInTheDocument();
    expect(within(steps[1]!).getByText("simulated")).toBeInTheDocument();
    expect(within(steps[1]!).queryByText(/ in · /)).not.toBeInTheDocument();
    expect(within(steps[2]!).getByText("✕ error")).toBeInTheDocument();
    expect(within(steps[2]!).getByText("30.0s")).toBeInTheDocument();
  });

  it("says so when nothing has been recorded", () => {
    renderWith(<Trace steps={[]} />);
    expect(screen.getByText("Nothing recorded yet.")).toBeInTheDocument();
  });
});

describe("RunsTable", () => {
  const run = {
    id: "run_06gk03byye9eqe9cfgg6dr67r0", status: "failed", trigger: "event:ticket.created", model: "m",
    costUsd: 0.02, activeMs: 2100, startedAt: new Date().toISOString(), error: "step limit reached", agent: { name: "support-triage" },
  };

  it("links each run to its trace and shows why a run failed", () => {
    renderWith(<RunsTable runs={[run]} showAgent />);
    expect(screen.getByRole("link", { name: "g6dr67r0" })).toHaveAttribute("href", `/runs/${run.id}`);
    expect(screen.getByRole("link", { name: "support-triage" })).toHaveAttribute("href", "/agents/support-triage");
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.getByText("step limit reached")).toBeInTheDocument();
  });

  it("omits the agent column on an agent's own page", () => {
    renderWith(<RunsTable runs={[run]} />);
    expect(screen.queryByRole("columnheader", { name: "Agent" })).not.toBeInTheDocument();
  });
});
