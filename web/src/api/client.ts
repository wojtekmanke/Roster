import { Client, cacheExchange, fetchExchange } from "urql";

const tokenKey = "roster.token";
const userKey = "roster.user";

/** The API token and display name live in the browser, never in the bundle. */
export const credentials = {
  get token(): string {
    return localStorage.getItem(tokenKey) ?? "";
  },
  get user(): string {
    return localStorage.getItem(userKey) ?? "";
  },
  save(token: string, user: string): void {
    localStorage.setItem(tokenKey, token.trim());
    localStorage.setItem(userKey, user.trim());
  },
};

/**
 * The GraphQL client. Headers are computed per request so a token saved in
 * the settings panel takes effect without a reload.
 */
export function createClient(): Client {
  return new Client({
    url: "/graphql",
    exchanges: [cacheExchange, fetchExchange],
    // The dashboard shows live operational state; a stale cache hit is worse
    // than a second request.
    requestPolicy: "cache-and-network",
    // Always POST. The client's default sends short queries as GET, which
    // would put every query and its variables into URLs and access logs.
    preferGetMethod: false,
    fetchOptions: () => {
      const headers: Record<string, string> = {};
      if (credentials.token) headers["Authorization"] = `Bearer ${credentials.token}`;
      // Recorded in the audit trail as the person who paused, resumed or approved.
      if (credentials.user) headers["X-Roster-User"] = credentials.user;
      return { headers };
    },
  });
}
