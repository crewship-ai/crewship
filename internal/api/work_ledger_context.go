package api

// work_ledger_context.go — #3012. The work ledger names what it holds.
//
// A work item and a webhook delivery carry ids: an agent, a crew, a delivery,
// a work item. The redesigned Work queue and Webhook deliveries say who did
// the work, in which crew, for which event, how long it took and what it cost,
// so each page is resolved here in one query per kind — never one per row.
//
// Every lookup is fenced to the workspace the list already answers for: the
// ids are untyped strings, and a collision across tenants must resolve to
// nothing rather than to someone else's agent. An agent or crew that no longer
// exists resolves to null, which the UI renders as "deleted" — the ledger's
// row outlives them on purpose.

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ledgerAgentRef is who a piece of work belongs to — enough to draw the
// agent's own avatar.
type ledgerAgentRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	AvatarSeed  string `json:"avatar_seed"`
	AvatarStyle string `json:"avatar_style"`
	// AvatarURL is the agent's stored render, null when it has none — the
	// same field the agents list carries, so the client draws the same face
	// and does not try to backfill one that is already stored.
	AvatarURL *string `json:"avatar_url"`
	// Deleted is true for an agent removed from the workspace whose row
	// remains (deleted_at): its name still says whose work it was.
	Deleted bool `json:"deleted"`
	// crewID backs the crew when the work item did not record one.
	crewID string
}

// ledgerCrewRef is the crew, with the colour and icon it wears elsewhere.
type ledgerCrewRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	Icon  string `json:"icon"`
	// Deleted is true for a crew removed from the workspace whose row remains.
	Deleted bool `json:"deleted"`
}

func uniqueNonEmpty(values []string) []any {
	seen := map[string]bool{}
	out := make([]any, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ledgerSince reads the since= filter: an RFC 3339 instant, rewritten in the
// ledger's own fixed-width layout so it compares with the stored text.
// ok is false when since was given and cannot be read.
func ledgerSince(raw string) (since string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", false
	}
	return tsformat.Format(t.UTC()), true
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func lookupLedgerAgents(ctx context.Context, db *sql.DB, workspaceID string, ids []string) (map[string]*ledgerAgentRef, error) {
	out := map[string]*ledgerAgentRef{}
	keys := uniqueNonEmpty(ids)
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(name,''), COALESCE(slug,''), COALESCE(avatar_seed,''), COALESCE(avatar_style,''), COALESCE(avatar_svg_hash,''), COALESCE(crew_id,''), deleted_at IS NOT NULL
		FROM agents WHERE workspace_id = ? AND id IN (`+placeholders(len(keys))+`)`,
		append([]any{workspaceID}, keys...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a ledgerAgentRef
		var hash string
		if err := rows.Scan(&a.ID, &a.Name, &a.Slug, &a.AvatarSeed, &a.AvatarStyle, &hash, &a.crewID, &a.Deleted); err != nil {
			return nil, err
		}
		a.AvatarURL = agentAvatarURL(a.ID, hash, workspaceID)
		out[a.ID] = &a
	}
	return out, rows.Err()
}

func lookupLedgerCrews(ctx context.Context, db *sql.DB, workspaceID string, ids []string) (map[string]*ledgerCrewRef, error) {
	out := map[string]*ledgerCrewRef{}
	keys := uniqueNonEmpty(ids)
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(name,''), COALESCE(color,''), COALESCE(icon,''), deleted_at IS NOT NULL
		FROM crews WHERE workspace_id = ? AND id IN (`+placeholders(len(keys))+`)`,
		append([]any{workspaceID}, keys...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c ledgerCrewRef
		if err := rows.Scan(&c.ID, &c.Name, &c.Color, &c.Icon, &c.Deleted); err != nil {
			return nil, err
		}
		out[c.ID] = &c
	}
	return out, rows.Err()
}

// attachWorkContext fills agent, crew, event and the attempts' duration and
// cost on one page of work items.
func attachWorkContext(ctx context.Context, db *sql.DB, workspaceID string, items []workItemView) error {
	if len(items) == 0 {
		return nil
	}
	agentIDs := make([]string, 0, len(items))
	crewIDs := make([]string, 0, len(items))
	deliveryIDs := make([]string, 0, len(items))
	workIDs := make([]any, 0, len(items))
	for _, it := range items {
		agentIDs = append(agentIDs, it.AgentID)
		crewIDs = append(crewIDs, it.CrewID)
		if it.Source == "webhook" {
			deliveryIDs = append(deliveryIDs, it.SourceRef)
		}
		workIDs = append(workIDs, it.ID)
	}
	// A replay of webhook work carries no delivery of its own: it is about the
	// event its original came in for. Walk replay_of back to that delivery.
	replayRef, err := replayedDeliveries(ctx, db, workspaceID, items)
	if err != nil {
		return err
	}
	for _, ref := range replayRef {
		deliveryIDs = append(deliveryIDs, ref)
	}
	agents, err := lookupLedgerAgents(ctx, db, workspaceID, agentIDs)
	if err != nil {
		return err
	}
	for _, a := range agents {
		crewIDs = append(crewIDs, a.crewID)
	}
	crews, err := lookupLedgerCrews(ctx, db, workspaceID, crewIDs)
	if err != nil {
		return err
	}
	events := map[string]string{}
	if keys := uniqueNonEmpty(deliveryIDs); len(keys) > 0 {
		rows, err := db.QueryContext(ctx, `
			SELECT id, COALESCE(event_type,'') FROM webhook_deliveries
			WHERE workspace_id = ? AND id IN (`+placeholders(len(keys))+`)`,
			append([]any{workspaceID}, keys...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, ev string
			if err := rows.Scan(&id, &ev); err != nil {
				rows.Close()
				return err
			}
			events[id] = ev
		}
		rows.Close()
	}
	type spend struct {
		first, last string
		cost        float64
	}
	spends := map[string]spend{}
	rows, err := db.QueryContext(ctx, `
		SELECT a.work_id, MIN(a.started_at), MAX(COALESCE(a.ended_at,'')), SUM(a.cost_usd)
		FROM work_attempts a
		JOIN work_items w ON w.id = a.work_id AND w.workspace_id = ?
		WHERE a.work_id IN (`+placeholders(len(workIDs))+`)
		GROUP BY a.work_id`, append([]any{workspaceID}, workIDs...)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var first, last sql.NullString
		var cost sql.NullFloat64
		if err := rows.Scan(&id, &first, &last, &cost); err != nil {
			rows.Close()
			return err
		}
		spends[id] = spend{first: first.String, last: last.String, cost: cost.Float64}
	}
	rows.Close()

	for i := range items {
		it := &items[i]
		it.Agent = agents[it.AgentID]
		crewID := it.CrewID
		if crewID == "" && it.Agent != nil {
			crewID = it.Agent.crewID
		}
		it.Crew = crews[crewID]
		if it.Source == "webhook" {
			ref := it.SourceRef
			if ref == "" {
				ref = replayRef[it.ID]
			}
			it.EventType = events[ref]
		}
		if sp, ok := spends[it.ID]; ok {
			// How long the work took is known once it has finished; while a
			// later attempt runs, an earlier attempt's end is not it.
			if terminal := it.TerminalAt != nil; terminal {
				if ms, ok := elapsedMS(sp.first, sp.last); ok {
					it.DurationMS = &ms
				}
			}
			// cost_usd is NOT NULL DEFAULT 0 per attempt, so a zero sum is
			// "no cost recorded", not a measured zero.
			if sp.cost > 0 {
				c := sp.cost
				it.CostUSD = &c
			}
		}
	}
	return nil
}

// nameWorkItem gives one work item the context the list gives a page — for the
// answers to resolve and replay, which a client may patch its cached row from.
func nameWorkItem(ctx context.Context, db *sql.DB, workspaceID string, item *workItemView) error {
	one := []workItemView{*item}
	if err := attachWorkContext(ctx, db, workspaceID, one); err != nil {
		return err
	}
	*item = one[0]
	return nil
}

// maxReplayDepth bounds the walk from a replay back to its original: a chain
// longer than this is not something the ledger produces by hand.
const maxReplayDepth = 8

// replayedDeliveries maps each webhook replay on the page (source_ref empty,
// replay_of set) to the delivery its original came from — one query per level
// of the chain, never one per row.
func replayedDeliveries(ctx context.Context, db *sql.DB, workspaceID string, items []workItemView) (map[string]string, error) {
	out := map[string]string{}
	// pending: the work id still to resolve -> the page items waiting on it.
	pending := map[string][]string{}
	for _, it := range items {
		if it.Source == "webhook" && it.SourceRef == "" && it.ReplayOf != nil && *it.ReplayOf != "" {
			pending[*it.ReplayOf] = append(pending[*it.ReplayOf], it.ID)
		}
	}
	for depth := 0; depth < maxReplayDepth && len(pending) > 0; depth++ {
		keys := make([]any, 0, len(pending))
		for id := range pending {
			keys = append(keys, id)
		}
		rows, err := db.QueryContext(ctx, `
			SELECT id, COALESCE(source_ref,''), COALESCE(replay_of,'') FROM work_items
			WHERE workspace_id = ? AND id IN (`+placeholders(len(keys))+`)`,
			append([]any{workspaceID}, keys...)...)
		if err != nil {
			return nil, err
		}
		next := map[string][]string{}
		for rows.Next() {
			var id, ref, replayOf string
			if err := rows.Scan(&id, &ref, &replayOf); err != nil {
				rows.Close()
				return nil, err
			}
			waiting := pending[id]
			switch {
			case ref != "":
				for _, w := range waiting {
					out[w] = ref
				}
			case replayOf != "":
				next[replayOf] = append(next[replayOf], waiting...)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		pending = next
	}
	return out, nil
}

// elapsedMS is the wall clock between the first attempt's start and the last
// attempt's end, when both are recorded.
func elapsedMS(first, last string) (int64, bool) {
	if first == "" || last == "" {
		return 0, false
	}
	a, err1 := time.Parse(time.RFC3339Nano, first)
	b, err2 := time.Parse(time.RFC3339Nano, last)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0, false
	}
	return b.Sub(a).Milliseconds(), true
}

// attachDeliveryContext names the endpoint's agent and the state of the work
// each delivery became.
func attachDeliveryContext(ctx context.Context, db *sql.DB, workspaceID string, items []webhookDeliveryView) error {
	if len(items) == 0 {
		return nil
	}
	agentIDs := make([]string, 0, len(items))
	workIDs := make([]string, 0, len(items))
	for _, it := range items {
		if it.EndpointKind == "agent" {
			agentIDs = append(agentIDs, it.EndpointID)
		}
		if it.WorkID != nil {
			workIDs = append(workIDs, *it.WorkID)
		}
	}
	agents, err := lookupLedgerAgents(ctx, db, workspaceID, agentIDs)
	if err != nil {
		return err
	}
	states := map[string]string{}
	if keys := uniqueNonEmpty(workIDs); len(keys) > 0 {
		rows, err := db.QueryContext(ctx, `
			SELECT id, state FROM work_items WHERE workspace_id = ? AND id IN (`+placeholders(len(keys))+`)`,
			append([]any{workspaceID}, keys...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, st string
			if err := rows.Scan(&id, &st); err != nil {
				rows.Close()
				return err
			}
			states[id] = st
		}
		rows.Close()
	}
	for i := range items {
		it := &items[i]
		if it.EndpointKind == "agent" {
			it.Agent = agents[it.EndpointID]
		}
		if it.WorkID != nil {
			if st, ok := states[*it.WorkID]; ok {
				s := st
				it.WorkState = &s
			}
		}
	}
	return nil
}
