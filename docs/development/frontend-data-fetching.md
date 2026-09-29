<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## Frontend data fetching

New or migrated client data hooks use **@tanstack/react-query** (the
client is wired in `components/providers.tsx`) instead of hand-rolled
`fetch` + `useState`. Reference implementations:
`hooks/use-dashboard-data.ts` and `hooks/use-inbox.ts`.

- **Query keys**: `[resource, workspaceId, params?]` — e.g.
  `["missions", wsId, { limit: 50 }]`. The workspace id at position 1
  isolates caches across workspace switches and lets
  `invalidateQueries({ queryKey: [resource, wsId] })` scope to one
  workspace. Export a small `…Keys` factory next to the hooks.
- **Transport**: always `apiFetch` from `lib/api-fetch.ts` (never bare
  `fetch`) so 401s go through the shared refresh-once-then-retry path.
  Pass React Query's `signal` through so unmounts abort the request.
- **Freshness**: where a WebSocket event exists (see
  `hooks/use-realtime.tsx`), subscribe with `useRealtimeEvent` and call
  `queryClient.invalidateQueries` — do not poll. A long
  `refetchInterval` (minutes, `refetchIntervalInBackground: false`) is
  acceptable only as a missed-event safety net.
- **Errors**: throw from the `queryFn` when the surface renders an
  error state; map non-ok responses to the slice's empty value for
  best-effort aggregate tiles (see the policy note in
  `hooks/use-dashboard-data.ts`).
- **Tests**: Vitest + `renderHook` with a fresh `QueryClient`
  (`retry: false, gcTime: 0`) per test — see
  `hooks/__tests__/use-dashboard-data.test.tsx`.
