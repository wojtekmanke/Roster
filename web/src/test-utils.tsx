import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { Provider, type Client } from "urql";
import { vi } from "vitest";
import { fromValue, merge, never } from "wonka";

export interface MockClient {
  client: Client;
  executeQuery: ReturnType<typeof vi.fn>;
  executeMutation: ReturnType<typeof vi.fn>;
}

/**
 * A stand-in for the GraphQL client. Queries answer with queryResult and
 * mutations with mutationResult; both record their calls.
 */
export function mockClient(queryResult: object, mutationResult: object = { data: {} }): MockClient {
  // A live query never completes, so neither does its stand-in: a source
  // that ends makes the hook discard an error-only result.
  const executeQuery = vi.fn(() => merge([fromValue(queryResult), never]));
  const executeMutation = vi.fn(() => fromValue(mutationResult));
  const client = { executeQuery, executeMutation, executeSubscription: vi.fn(() => never) } as unknown as Client;
  return { client, executeQuery, executeMutation };
}

/** Renders a component with the router and GraphQL client it expects. */
export function renderWith(ui: ReactElement, client: Client = mockClient({ data: {} }).client) {
  return render(
    <Provider value={client}>
      <MemoryRouter>{ui}</MemoryRouter>
    </Provider>,
  );
}
