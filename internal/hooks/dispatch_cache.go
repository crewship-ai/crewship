package hooks

import "sync"

// negativeDispatchCache remembers (workspace_id, crew_id, event) triples
// that had zero enabled hooks_config rows the last time Dispatch checked,
// so the hot path — every LLM call, every observed tool call, every
// delegation, every peer query, every budget breach — can skip
// ListByEvent entirely for the overwhelmingly common case of "no hooks
// registered here". See #2154.
//
// Only the negative result is cached. A workspace WITH matching hooks
// still pays the query on every Dispatch call: the Matcher pass that
// follows ListByEvent is per-call (it looks at ec.AgentID, ec.MissionID,
// tags, tool name, etc., all of which vary call to call), so a cached row
// set would either need to reproduce that filtering here or risk serving
// a stale "matches" decision. Caching "there is definitely nothing to
// filter" sidesteps that problem entirely; caching "here are the rows,
// filter them yourself" does not, so it isn't attempted.
//
// Invalidation is per-workspace and coarse: any successful write in
// store.go (Register, Update, Delete, SetEnabled) drops every cached
// entry for that workspace_id, not just the (crew_id, event) pair the
// write touched. Precise invalidation would need to know the row's OLD
// crew_id/event too — Update can change both, and by design doesn't tell
// the caller what the row looked like before. Getting that wrong produces
// a false negative (a hook that silently never fires), which is worse
// than the handful of extra queries a coarse invalidation costs right
// after a write. Writes are rare next to dispatches, so the imprecision
// is cheap.
//
// Single-process only, same caveat as paymaster's enforceLocks: a second
// crewshipd process writing hooks_config would not see this process's
// cache go stale until this process's own next write here, or until this
// process restarts. The issue calls out a per-write version counter as a
// possible follow-up without an obvious win; today there is one writer
// process, so it's out of scope for this fix.
var negativeDispatchCache sync.Map // negativeCacheKey → struct{}

// negativeCacheKey mirrors the (workspace_id, crew_id, event) triple
// ListByEvent is called with — the exact shape Dispatch already has in
// hand from ec and event, so building the key costs nothing extra.
type negativeCacheKey struct {
	WorkspaceID string
	CrewID      string
	Event       Event
}

// InvalidateCache drops every cached negative-dispatch entry for
// workspaceID. Register, Update, Delete and SetEnabled in store.go call
// this after every successful write to hooks_config. Exported so a
// hooks_config writer introduced outside this package in the future has
// somewhere correct to route through, rather than reaching into the cache
// (or forgetting to invalidate it) directly.
func InvalidateCache(workspaceID string) {
	negativeDispatchCache.Range(func(k, _ any) bool {
		if key, ok := k.(negativeCacheKey); ok && key.WorkspaceID == workspaceID {
			negativeDispatchCache.Delete(key)
		}
		return true
	})
}
