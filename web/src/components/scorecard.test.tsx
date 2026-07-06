import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWith } from "../test-utils";
import { ComparisonTable, KpiTable, type Kpi, type ModelScorecard } from "./scorecard";

const kpis: Kpi[] = [
  { name: "right_call", grader: "expected", aggregate: "mean", value: 0.6667, samples: 30, target: ">= 0.95", status: "breach" },
  { name: "cost_per_ticket", grader: "cost", aggregate: "mean", value: 0.0123, samples: 30, target: "<= 0.03", status: "ok" },
  { name: "precision", grader: "classification", aggregate: "mean", value: 1, samples: 4, target: ">= 0.9", status: "insufficient_data" },
  { name: "p95_seconds", grader: "latency", aggregate: "p95", value: 4.2, samples: 30, target: null, status: "no_target" },
  { name: "quality", grader: "judge", aggregate: "mean", value: null, samples: 0, target: ">= 4", status: "insufficient_data" },
];

function row(name: string) {
  return within(screen.getByText(name).closest("tr")!);
}

describe("KpiTable", () => {
  it("shows each KPI's value in its own unit, with its target and standing", () => {
    renderWith(<KpiTable kpis={kpis} minRuns={20} />);

    expect(row("right_call").getByText("0.667")).toBeInTheDocument();
    expect(row("right_call").getByText(">= 0.95")).toBeInTheDocument();
    expect(row("right_call").getByText("Missed")).toBeInTheDocument();

    expect(row("cost_per_ticket").getByText("$0.01")).toBeInTheDocument();
    expect(row("cost_per_ticket").getByText("On target")).toBeInTheDocument();

    expect(row("p95_seconds").getByText("4.2s")).toBeInTheDocument();
    expect(row("p95_seconds").getByText("Tracked")).toBeInTheDocument();
  });

  it("says how many runs a KPI still needs before it can be judged", () => {
    renderWith(<KpiTable kpis={kpis} minRuns={20} />);
    expect(row("precision").getByText("Too few runs")).toBeInTheDocument();
    expect(row("precision").getByText("of 20 needed")).toBeInTheDocument();
    expect(row("right_call").queryByText("of 20 needed")).not.toBeInTheDocument();
  });

  it("shows a dash, not a zero, for a KPI with no samples", () => {
    renderWith(<KpiTable kpis={kpis} minRuns={20} />);
    const cells = row("quality").getAllByRole("cell");
    expect(cells[1]).toHaveTextContent("–");
  });

  it("explains an empty scorecard", () => {
    renderWith(<KpiTable kpis={[]} minRuns={20} />);
    expect(screen.getByText(/has no KPIs/)).toBeInTheDocument();
  });
});

describe("ComparisonTable", () => {
  const models: ModelScorecard[] = [
    { provider: "anthropic", model: "claude-opus-5-5", shadow: false, runs: 40, succeeded: 40, avgCostUsd: 0.021, avgSeconds: 3.4, scorecard: { kpis: kpis.slice(0, 2).map((k) => ({ ...k, value: k.name === "right_call" ? 0.98 : 0.021, status: "ok" })) } },
    { provider: "ollama", model: "llama3.2", shadow: true, runs: 40, succeeded: 36, avgCostUsd: 0, avgSeconds: 9.1, scorecard: { kpis: kpis.slice(0, 2) } },
  ];

  it("puts every model's KPIs side by side and marks which is the shadow", () => {
    renderWith(<ComparisonTable models={models} />);

    const primary = row("claude-opus-5-5");
    expect(primary.getByText("primary")).toBeInTheDocument();
    expect(primary.getByText("100%")).toBeInTheDocument();
    expect(primary.getByText("0.98")).toBeInTheDocument();

    const shadow = row("llama3.2");
    expect(shadow.getByText("shadow")).toBeInTheDocument();
    expect(shadow.getByText("90%")).toBeInTheDocument();
    expect(shadow.getByText("$0")).toBeInTheDocument();
    expect(shadow.getByText("9.1s")).toBeInTheDocument();
  });

  it("flags a missed target with a mark, not colour alone", () => {
    renderWith(<ComparisonTable models={models} />);
    expect(row("llama3.2").getByTitle("Misses the target")).toHaveTextContent("✕");
    expect(row("claude-opus-5-5").queryByTitle("Misses the target")).not.toBeInTheDocument();
  });

  it("explains an empty comparison", () => {
    renderWith(<ComparisonTable models={[]} />);
    expect(screen.getByText(/No finished runs/)).toBeInTheDocument();
  });
});
