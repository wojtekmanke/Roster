import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { mockClient, renderWith } from "../test-utils";
import { ApprovalCard, type ApprovalItem } from "./ApprovalCard";

const pending: ApprovalItem = {
  id: "apr_1",
  tool: "create_purchase_order",
  input: { sku: "SKU-104", quantity: 72, note: "4 a day, 7 day lead time" },
  status: "pending",
  createdAt: new Date().toISOString(),
  agent: { name: "reorder-watcher" },
  run: { id: "run_abcdefgh12345678" },
};

function variables(mock: ReturnType<typeof mockClient>) {
  const [request] = mock.executeMutation.mock.calls[0] as unknown as [{ variables: unknown }];
  return request.variables;
}

describe("ApprovalCard", () => {
  it("shows exactly what is being approved", () => {
    renderWith(<ApprovalCard approval={pending} onDecided={() => {}} />);
    expect(screen.getByText("create_purchase_order")).toBeInTheDocument();
    expect(screen.getByText(/"quantity": 72/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "reorder-watcher" })).toHaveAttribute("href", "/agents/reorder-watcher");
    expect(screen.getByRole("link", { name: "See the run" })).toHaveAttribute("href", "/runs/run_abcdefgh12345678");
  });

  it("has no dangling separator when shown on the run's own page", () => {
    const { container } = renderWith(<ApprovalCard approval={{ ...pending, run: null }} onDecided={() => {}} />);
    expect(screen.queryByRole("link", { name: "See the run" })).not.toBeInTheDocument();
    expect(container.querySelector(".card-head .small")?.textContent).toBe("just now");
  });

  it("approves without needing a note", async () => {
    const mock = mockClient({ data: {} }, { data: { decideApproval: { id: "apr_1", status: "approved" } } });
    const onDecided = vi.fn();
    renderWith(<ApprovalCard approval={pending} onDecided={onDecided} />, mock.client);

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    expect(variables(mock)).toEqual({ id: "apr_1", approve: true, reason: null });
    expect(onDecided).toHaveBeenCalledOnce();
  });

  it("will not reject without a reason", async () => {
    const mock = mockClient({ data: {} });
    const onDecided = vi.fn();
    renderWith(<ApprovalCard approval={pending} onDecided={onDecided} />, mock.client);

    await userEvent.click(screen.getByRole("button", { name: "Reject" }));

    expect(screen.getByRole("alert")).toHaveTextContent("Say why");
    expect(mock.executeMutation).not.toHaveBeenCalled();
    expect(onDecided).not.toHaveBeenCalled();
  });

  it("rejects with the reviewer's reason", async () => {
    const mock = mockClient({ data: {} }, { data: { decideApproval: { id: "apr_1", status: "rejected" } } });
    renderWith(<ApprovalCard approval={pending} onDecided={() => {}} />, mock.client);

    await userEvent.type(screen.getByRole("textbox"), "  Supplier is out of stock ");
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));

    expect(variables(mock)).toEqual({ id: "apr_1", approve: false, reason: "Supplier is out of stock" });
  });

  it("shows the server's error and stays open when the decision fails", async () => {
    const mock = mockClient({ data: {} }, { error: { message: "approval apr_1 has already been decided" } });
    const onDecided = vi.fn();
    renderWith(<ApprovalCard approval={pending} onDecided={onDecided} />, mock.client);

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    expect(screen.getByRole("alert")).toHaveTextContent("already been decided");
    expect(onDecided).not.toHaveBeenCalled();
  });

  it("shows who decided, and has no buttons once decided", () => {
    renderWith(
      <ApprovalCard
        approval={{ ...pending, status: "rejected", decidedBy: "wojtek", reason: "duplicate order" }}
        onDecided={() => {}}
      />,
    );
    expect(screen.getByText("Rejected")).toBeInTheDocument();
    expect(screen.getByText("by wojtek: duplicate order")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });
});
