import { QueryClient } from "@tanstack/react-query";

/** Shared TanStack Query client. Freshness comes from the event stream (see events.ts), not polling. */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});
