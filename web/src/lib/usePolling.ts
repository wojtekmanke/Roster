import { useEffect, useRef } from "react";

/**
 * Calls refresh every intervalMs while the tab is visible. The dashboard
 * polls rather than subscribes: runs take seconds, and a five-second lag on a
 * scorecard changes nothing a person would do.
 */
export function usePolling(refresh: () => void, intervalMs = 5000): void {
  const latest = useRef(refresh);
  latest.current = refresh;

  useEffect(() => {
    const id = setInterval(() => {
      if (document.visibilityState === "visible") latest.current();
    }, intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
}
