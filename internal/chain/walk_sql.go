package chain

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ---------------------------------------------------------------------------
// Node constructors.
//
// Every node is built from a row this package selected with the workspace in
// the predicate, so a Node value is by construction in-tenant. The Partial
// flags are set here rather than at the call sites so a kind cannot gain a new
// discovery path that forgets to declare its known blind spot.
// ---------------------------------------------------------------------------

func issueNode(id, identifier, title, status string) Node {
	key := identifier
	if key == "" {
		key = id
	}
	return Node{
		ID:     nodeID(KindIssue, id),
		Kind:   KindIssue,
		Ref:    id,
		Key:    key,
		Label:  title,
		Status: status,
		// See KnownGaps[0]: nothing in inbox_items points at a mission, so
		// the approvals and failure alerts raised while this issue was worked
		// are not reachable from it.
		Partial:       true,
		PartialReason: "inbox items raised while this issue was worked cannot be linked to it: inbox_items carries no mission/issue column.",
	}
}

func routineNode(id, name, slug, status string) Node {
	label := name
	if label == "" {
		label = slug
	}
	return Node{
		ID:     nodeID(KindRoutine, id),
		Kind:   KindRoutine,
		Ref:    id,
		Key:    slug,
		Label:  label,
		Status: status,
	}
}

func runNode(id, pipelineSlug, status string) Node {
	return Node{
		ID:     nodeID(KindRun, id),
		Kind:   KindRun,
		Ref:    id,
		Key:    pipelineSlug,
		Label:  pipelineSlug,
		Status: status,
	}
}

func assignmentNode(id, task, status string) Node {
	return Node{
		ID:     nodeID(KindAssignment, id),
		Kind:   KindAssignment,
		Ref:    id,
		Label:  task,
		Status: status,
	}
}

func agentNode(id, name, slug, status string) Node {
	label := name
	if label == "" {
		label = slug
	}
	return Node{
		ID:     nodeID(KindAgent, id),
		Kind:   KindAgent,
		Ref:    id,
		Key:    slug,
		Label:  label,
		Status: status,
		// An agent is a chain leaf on purpose. Expanding it would pull in
		// every assignment that agent has ever taken, which is that agent's
		// history rather than this chain — the graph would stop being "what
		// caused what" and become "everything this workspace has ever done".
		Partial:       true,
		PartialReason: "agents are chain leaves: this agent's other assignments are not part of this chain.",
	}
}

// inboxPartial explains, per kind, why an inbox item may be a dead end.
// 'waitpoint' and 'failed_run' carry a run pointer and are fully walkable;
// every other kind does not, and says so.
func inboxPartial(kind string) (bool, string) {
	switch kind {
	case "waitpoint", "failed_run":
		return false, ""
	case "escalation":
		// KnownGaps[1].
		return true, "escalations has neither a run nor a mission column, so this escalation cannot be traced back to what provoked it."
	default:
		return true, "inbox kind " + kind + " carries no run or issue pointer in source_id, so it is a chain leaf."
	}
}

// automationNode builds the rule node. automations has no status column, so
// Status is derived: a soft-deleted rule reads "deleted" rather than
// disappearing, because the runs it fired still point at it and "what caused
// this" must keep answering after the rule is gone — which is the reason the
// table soft-deletes at all (AutomationHandler.Delete).
func automationNode(id, name, eventType string, enabled bool, deleted bool) Node {
	status := "enabled"
	switch {
	case deleted:
		status = "deleted"
	case !enabled:
		status = "disabled"
	}
	return Node{
		ID:     nodeID(KindAutomation, id),
		Kind:   KindAutomation,
		Ref:    id,
		Key:    eventType,
		Label:  name,
		Status: status,
	}
}

func inboxNode(id, kind, title, state string) Node {
	partial, reason := inboxPartial(kind)
	return Node{
		ID:            nodeID(KindInbox, id),
		Kind:          KindInbox,
		Ref:           id,
		Key:           kind,
		Label:         title,
		Status:        state,
		Partial:       partial,
		PartialReason: reason,
	}
}

// ---------------------------------------------------------------------------
// Anchor resolution.
// ---------------------------------------------------------------------------

// resolveAnchor maps a free-form anchor string onto exactly one row.
//
// The order is by discriminating power, not by preference: an issue identifier
// ("ENG-4") cannot collide with anything else, run and pipeline ids carry
// distinct prefixes, and the id lookups come after so a slug that happens to
// look like an id still resolves the way a human meant it.
func (w *walker) resolveAnchor(ctx context.Context, anchor string) (Node, error) {
	lookups := []func(context.Context, string) (Node, bool, error){
		w.lookupIssueByIdentifier,
		w.lookupRunByID,
		w.lookupIssueByID,
		w.lookupRoutineByID,
		w.lookupRoutineBySlug,
		w.lookupAssignmentByID,
		w.lookupAutomationByID,
		w.lookupInboxByID,
	}
	for _, fn := range lookups {
		n, ok, err := fn(ctx, anchor)
		if err != nil {
			return Node{}, err
		}
		if ok {
			return n, nil
		}
	}
	return Node{}, ErrAnchorNotFound
}

func (w *walker) lookupIssueByIdentifier(ctx context.Context, anchor string) (Node, bool, error) {
	var id, identifier, title, status string
	err := w.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(identifier,''), title, status
		FROM missions
		WHERE workspace_id = ? AND identifier = ?`,
		w.workspaceID, strings.ToUpper(anchor),
	).Scan(&id, &identifier, &title, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return issueNode(id, identifier, title, status), true, nil
}

func (w *walker) lookupIssueByID(ctx context.Context, anchor string) (Node, bool, error) {
	var id, identifier, title, status string
	err := w.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(identifier,''), title, status
		FROM missions
		WHERE workspace_id = ? AND id = ?`,
		w.workspaceID, anchor,
	).Scan(&id, &identifier, &title, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return issueNode(id, identifier, title, status), true, nil
}

func (w *walker) lookupRunByID(ctx context.Context, anchor string) (Node, bool, error) {
	var id, slug, status string
	err := w.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(pipeline_slug,''), COALESCE(status,'')
		FROM pipeline_runs
		WHERE workspace_id = ? AND id = ?`,
		w.workspaceID, anchor,
	).Scan(&id, &slug, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return runNode(id, slug, status), true, nil
}

func (w *walker) lookupRoutineByID(ctx context.Context, anchor string) (Node, bool, error) {
	return w.lookupRoutine(ctx, `id = ?`, anchor)
}

func (w *walker) lookupRoutineBySlug(ctx context.Context, anchor string) (Node, bool, error) {
	return w.lookupRoutine(ctx, `slug = ?`, anchor)
}

// lookupRoutine takes its predicate as a constant expression from the two
// callers above — never from request data — so the concatenation cannot carry
// caller-controlled SQL.
func (w *walker) lookupRoutine(ctx context.Context, pred, arg string) (Node, bool, error) {
	var id, name, slug, status string
	err := w.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(name,''), COALESCE(slug,''), COALESCE(status,'')
		FROM pipelines
		WHERE workspace_id = ? AND deleted_at IS NULL AND `+pred,
		w.workspaceID, arg,
	).Scan(&id, &name, &slug, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return routineNode(id, name, slug, status), true, nil
}

func (w *walker) lookupAssignmentByID(ctx context.Context, anchor string) (Node, bool, error) {
	var id, task, status string
	err := w.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(task,''), COALESCE(status,'')
		FROM assignments
		WHERE workspace_id = ? AND id = ?`,
		w.workspaceID, anchor,
	).Scan(&id, &task, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return assignmentNode(id, task, status), true, nil
}

// lookupAutomationByID resolves a rule. Deliberately NOT filtered on
// deleted_at, unlike lookupRoutine: a soft-deleted rule is exactly the one an
// operator is trying to identify when they ask why a run happened, and hiding
// it would turn "a deleted rule did this" into "nothing did this".
func (w *walker) lookupAutomationByID(ctx context.Context, anchor string) (Node, bool, error) {
	var id, name, eventType string
	var enabled int
	var deletedAt sql.NullString
	err := w.db.QueryRowContext(ctx, `
		SELECT id, name, event_type, enabled, deleted_at
		FROM automations
		WHERE workspace_id = ? AND id = ?`,
		w.workspaceID, anchor,
	).Scan(&id, &name, &eventType, &enabled, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return automationNode(id, name, eventType, enabled != 0, deletedAt.Valid), true, nil
}

func (w *walker) lookupInboxByID(ctx context.Context, anchor string) (Node, bool, error) {
	var id, kind, title, state string
	err := w.db.QueryRowContext(ctx, `
		SELECT id, kind, title, COALESCE(state,'')
		FROM inbox_items
		WHERE workspace_id = ? AND id = ?`,
		w.workspaceID, anchor,
	).Scan(&id, &kind, &title, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return inboxNode(id, kind, title, state), true, nil
}

// ---------------------------------------------------------------------------
// Expansion.
//
// One method per node kind. Each returns every neighbour reachable by a real
// column, in a stable order, with the workspace in every predicate.
// ---------------------------------------------------------------------------

// expandIssue walks missions.routine_id (the issue→routine binding),
// pipeline_runs.triggered_via='issue' (runs fired from this issue),
// mission_tasks.assignment_id (work dispatched as an assignment) and
// mission_relations (author-declared issue↔issue links).
//
// It cannot walk to inbox_items — see KnownGaps[0], reflected on the node.
func (w *walker) expandIssue(ctx context.Context, n Node) ([]neighbour, error) {
	var out []neighbour

	// missions.routine_id -> pipelines.id. No FK (SQLite ALTER TABLE cannot
	// add one), so the workspace predicate on pipelines is what keeps a stale
	// or cross-tenant id from resolving.
	if err := w.collect(ctx, &out, `
		SELECT p.id, COALESCE(p.name,''), COALESCE(p.slug,''), COALESCE(p.status,'')
		FROM missions m
		JOIN pipelines p
		  ON p.id = m.routine_id
		 AND p.workspace_id = m.workspace_id
		 AND p.deleted_at IS NULL
		WHERE m.id = ? AND m.workspace_id = ?`,
		[]any{n.Ref, w.workspaceID},
		func(rows *sql.Rows) (neighbour, error) {
			var id, name, slug, status string
			if err := rows.Scan(&id, &name, &slug, &status); err != nil {
				return neighbour{}, err
			}
			to := routineNode(id, name, slug, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	// pipeline_runs.triggered_by_id holds the issue IDENTIFIER (not the
	// mission id) when triggered_via='issue' — hence the join on
	// m.identifier. Identifiers are only unique per workspace since the
	// 20260806203901 migration, which is exactly why r.workspace_id is in the
	// join and not merely in the WHERE.
	if err := w.collect(ctx, &out, `
		SELECT r.id, COALESCE(r.pipeline_slug,''), COALESCE(r.status,'')
		FROM missions m
		JOIN pipeline_runs r
		  ON r.triggered_by_id = m.identifier
		 AND r.workspace_id = m.workspace_id
		WHERE m.id = ? AND m.workspace_id = ? AND r.triggered_via = 'issue'
		ORDER BY r.started_at DESC, r.id ASC
		LIMIT ?`,
		[]any{n.Ref, w.workspaceID, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, slug, status string
			if err := rows.Scan(&id, &slug, &status); err != nil {
				return neighbour{}, err
			}
			to := runNode(id, slug, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	// The issue→assignment link lives on mission_tasks.assignment_id, not on
	// assignments — assignments has no mission column at all. mission_tasks
	// has no workspace_id either, so the tenant fence is the join to missions
	// and to assignments, both carrying it.
	if err := w.collect(ctx, &out, `
		SELECT a.id, COALESCE(a.task,''), COALESCE(a.status,'')
		FROM mission_tasks mt
		JOIN missions m ON m.id = mt.mission_id
		JOIN assignments a
		  ON a.id = mt.assignment_id
		 AND a.workspace_id = m.workspace_id
		WHERE mt.mission_id = ? AND m.workspace_id = ?
		ORDER BY mt.task_order ASC, a.id ASC
		LIMIT ?`,
		[]any{n.Ref, w.workspaceID, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, task, status string
			if err := rows.Scan(&id, &task, &status); err != nil {
				return neighbour{}, err
			}
			to := assignmentNode(id, task, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	// mission_relations, both directions. The edge always points source→target
	// so "blocks" keeps its meaning regardless of which end the walk arrived
	// from.
	for _, dir := range []struct{ selfCol, otherCol string }{
		{"source_id", "target_id"},
		{"target_id", "source_id"},
	} {
		selfCol, otherCol := dir.selfCol, dir.otherCol
		if err := w.collect(ctx, &out, `
			SELECT m.id, COALESCE(m.identifier,''), m.title, m.status
			FROM mission_relations rel
			JOIN missions m ON m.id = rel.`+otherCol+` AND m.workspace_id = ?
			WHERE rel.`+selfCol+` = ?
			ORDER BY m.id ASC
			LIMIT ?`,
			[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
			func(rows *sql.Rows) (neighbour, error) {
				var id, identifier, title, status string
				if err := rows.Scan(&id, &identifier, &title, &status); err != nil {
					return neighbour{}, err
				}
				other := issueNode(id, identifier, title, status)
				e := Edge{From: n.ID, To: other.ID, Kind: EdgeRelates}
				if selfCol == "target_id" {
					e = Edge{From: other.ID, To: n.ID, Kind: EdgeRelates}
				}
				return neighbour{node: other, edge: e}, nil
			}); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// expandRoutine walks pipeline_runs.pipeline_id (a real FK) and the reverse of
// missions.routine_id.
func (w *walker) expandRoutine(ctx context.Context, n Node) ([]neighbour, error) {
	var out []neighbour

	if err := w.collect(ctx, &out, `
		SELECT id, COALESCE(pipeline_slug,''), COALESCE(status,'')
		FROM pipeline_runs
		WHERE workspace_id = ? AND pipeline_id = ?
		ORDER BY started_at DESC, id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, slug, status string
			if err := rows.Scan(&id, &slug, &status); err != nil {
				return neighbour{}, err
			}
			to := runNode(id, slug, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeRuns}}, nil
		}); err != nil {
		return nil, err
	}

	if err := w.collect(ctx, &out, `
		SELECT id, COALESCE(identifier,''), title, status
		FROM missions
		WHERE workspace_id = ? AND routine_id = ?
		ORDER BY id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, identifier, title, status string
			if err := rows.Scan(&id, &identifier, &title, &status); err != nil {
				return neighbour{}, err
			}
			from := issueNode(id, identifier, title, status)
			return neighbour{node: from, edge: Edge{From: from.ID, To: n.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	// The reverse of expandAutomation's slug join: which rules start this
	// routine. Present because direction is a property of the RELATIONSHIP,
	// not of which end the walk began at — an asymmetric hop would make a
	// chain anchored on the routine disagree with the same chain anchored on
	// the rule, which is the whole failure mode `neighbour` carries its own
	// From/To to prevent. Soft-deleted rules are included for the same reason
	// lookupAutomationByID includes them.
	if err := w.collect(ctx, &out, `
		SELECT a.id, a.name, a.event_type, a.enabled, a.deleted_at
		FROM automations a
		JOIN pipelines p
		  ON p.slug = json_extract(a.action_config_json, '$.routine_slug')
		 AND p.workspace_id = a.workspace_id
		WHERE p.id = ? AND a.workspace_id = ? AND json_valid(a.action_config_json)
		ORDER BY a.id ASC
		LIMIT ?`,
		[]any{n.Ref, w.workspaceID, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, name, eventType string
			var enabled int
			var deletedAt sql.NullString
			if err := rows.Scan(&id, &name, &eventType, &enabled, &deletedAt); err != nil {
				return neighbour{}, err
			}
			from := automationNode(id, name, eventType, enabled != 0, deletedAt.Valid)
			return neighbour{node: from, edge: Edge{From: from.ID, To: n.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	return out, nil
}

// expandRun walks pipeline_runs.pipeline_id (up to the routine), the
// triggered_via/triggered_by_id pair (up to the issue or the parent run), the
// reverse of that pair (down to nested call_pipeline runs), and the two inbox
// kinds that carry a run pointer.
func (w *walker) expandRun(ctx context.Context, n Node) ([]neighbour, error) {
	var out []neighbour

	var pipelineID, triggeredVia, triggeredByID string
	err := w.db.QueryRowContext(ctx, `
		SELECT COALESCE(pipeline_id,''), COALESCE(triggered_via,''), COALESCE(triggered_by_id,'')
		FROM pipeline_runs
		WHERE id = ? AND workspace_id = ?`,
		n.Ref, w.workspaceID,
	).Scan(&pipelineID, &triggeredVia, &triggeredByID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if pipelineID != "" {
		if rn, ok, err := w.lookupRoutine(ctx, `id = ?`, pipelineID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, neighbour{node: rn, edge: Edge{From: rn.ID, To: n.ID, Kind: EdgeRuns}})
		}
	}

	// triggered_by_id is polymorphic — a schedule id, a webhook id, a parent
	// run id or an issue identifier — so it is only ever dereferenced against
	// the table triggered_via names. Following it blind is how a chain grows a
	// fabricated edge.
	switch {
	case triggeredVia == "issue" && triggeredByID != "":
		if in, ok, err := w.lookupIssueByIdentifier(ctx, triggeredByID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, neighbour{node: in, edge: Edge{From: in.ID, To: n.ID, Kind: EdgeTriggers}})
		}
	case triggeredVia == "call_pipeline" && triggeredByID != "":
		if pn, ok, err := w.lookupRunByID(ctx, triggeredByID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, neighbour{node: pn, edge: Edge{From: pn.ID, To: n.ID, Kind: EdgeTriggers}})
		}
	case triggeredVia == "automation" && triggeredByID != "":
		// The rule that fired this run. The automation registry stamps
		// (triggered_via, triggered_by_id) on the pending_runs row and the
		// dispatcher carries it onto the run, so this is a real column and not
		// an inference — which is why it is walked rather than declared a gap.
		if an, ok, err := w.lookupAutomationByID(ctx, triggeredByID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, neighbour{node: an, edge: Edge{From: an.ID, To: n.ID, Kind: EdgeTriggers}})
		}
	}

	// Nested runs this run started.
	if err := w.collect(ctx, &out, `
		SELECT id, COALESCE(pipeline_slug,''), COALESCE(status,'')
		FROM pipeline_runs
		WHERE workspace_id = ? AND triggered_via = 'call_pipeline' AND triggered_by_id = ?
		ORDER BY started_at ASC, id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, slug, status string
			if err := rows.Scan(&id, &slug, &status); err != nil {
				return neighbour{}, err
			}
			to := runNode(id, slug, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	// Which agents actually did the work in this run.
	//
	// There is no agent column on pipeline_runs that survives the DAG —
	// invoking_agent_id names who STARTED the run, not who executed its
	// steps — so the journal is the only record of it, correlated the way
	// the run-logs endpoint does it: pipeline runs tag their entries with
	// payload.run_id (surfaced as the VIRTUAL generated column run_id, v120),
	// while agent-driven runs use trace_id instead. Both arms are index-
	// unionable (idx_journal_ws_run).
	//
	// This is a direct OR against journal_entries rather than two
	// journal.Query calls merged in Go: journal.Query ANDs its fields, so the
	// package-level API cannot express "either", and issuing two queries
	// would double the round trips to reconstruct one index union SQLite
	// already does.
	if err := w.collect(ctx, &out, `
		SELECT DISTINCT a.id, COALESCE(a.name,''), COALESCE(a.slug,''), COALESCE(a.status,'')
		FROM journal_entries j
		JOIN agents a ON a.id = j.actor_id AND a.workspace_id = j.workspace_id
		WHERE j.workspace_id = ?
		  AND j.actor_type = 'agent'
		  AND (j.trace_id = ? OR j.run_id = ?)
		ORDER BY a.id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, n.Ref, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, name, slug, status string
			if err := rows.Scan(&id, &name, &slug, &status); err != nil {
				return neighbour{}, err
			}
			an := agentNode(id, name, slug, status)
			return neighbour{node: an, edge: Edge{From: an.ID, To: n.ID, Kind: EdgeExecutes}}, nil
		}); err != nil {
		return nil, err
	}

	// Inbox, kind 'waitpoint': inbox_items.source_id is the waitpoint TOKEN,
	// and pipeline_waitpoints.pipeline_run_id is what names the run.
	if err := w.collect(ctx, &out, `
		SELECT i.id, i.kind, i.title, COALESCE(i.state,'')
		FROM inbox_items i
		JOIN pipeline_waitpoints wp
		  ON wp.token = i.source_id
		 AND wp.workspace_id = i.workspace_id
		WHERE i.workspace_id = ? AND i.kind = 'waitpoint' AND wp.pipeline_run_id = ?
		ORDER BY i.id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		w.scanInboxNeighbour(n.ID)); err != nil {
		return nil, err
	}

	// Inbox, kind 'failed_run': the run id is in the payload, not source_id.
	if err := w.collect(ctx, &out, `
		SELECT id, kind, title, COALESCE(state,'')
		FROM inbox_items
		WHERE workspace_id = ?
		  AND kind = 'failed_run'
		  AND json_valid(payload_json)
		  AND json_extract(payload_json, '$.run_id') = ?
		ORDER BY id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		w.scanInboxNeighbour(n.ID)); err != nil {
		return nil, err
	}

	return out, nil
}

// expandAutomation walks the runs this rule fired
// (pipeline_runs.triggered_via='automation') and the routine it targets
// (action_config_json -> routine_slug, resolved against pipelines).
//
// Both edges are EdgeTriggers, and they are the same relation at definition
// and instance level — exactly the pair expandIssue already emits for
// missions.routine_id and for the runs an issue fired. Naming them differently
// would make "this rule starts that routine" and "this rule started that run"
// look like two unrelated facts.
func (w *walker) expandAutomation(ctx context.Context, n Node) ([]neighbour, error) {
	var out []neighbour

	if err := w.collect(ctx, &out, `
		SELECT id, COALESCE(pipeline_slug,''), COALESCE(status,'')
		FROM pipeline_runs
		WHERE workspace_id = ? AND triggered_via = 'automation' AND triggered_by_id = ?
		ORDER BY started_at DESC, id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, slug, status string
			if err := rows.Scan(&id, &slug, &status); err != nil {
				return neighbour{}, err
			}
			to := runNode(id, slug, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	// The rule names its target by SLUG, so the join is on pipelines.slug with
	// the workspace on BOTH sides — a slug is only unique per workspace, and
	// this is precisely the shape that would otherwise pull a foreign routine
	// into the graph.
	if err := w.collect(ctx, &out, `
		SELECT p.id, COALESCE(p.name,''), COALESCE(p.slug,''), COALESCE(p.status,'')
		FROM automations a
		JOIN pipelines p
		  ON p.slug = json_extract(a.action_config_json, '$.routine_slug')
		 AND p.workspace_id = a.workspace_id
		 AND p.deleted_at IS NULL
		WHERE a.id = ? AND a.workspace_id = ? AND json_valid(a.action_config_json)
		LIMIT ?`,
		[]any{n.Ref, w.workspaceID, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, name, slug, status string
			if err := rows.Scan(&id, &name, &slug, &status); err != nil {
				return neighbour{}, err
			}
			to := routineNode(id, name, slug, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	return out, nil
}

func (w *walker) scanInboxNeighbour(fromID string) func(*sql.Rows) (neighbour, error) {
	return func(rows *sql.Rows) (neighbour, error) {
		var id, kind, title, state string
		if err := rows.Scan(&id, &kind, &title, &state); err != nil {
			return neighbour{}, err
		}
		to := inboxNode(id, kind, title, state)
		return neighbour{node: to, edge: Edge{From: fromID, To: to.ID, Kind: EdgeProduces}}, nil
	}
}

// expandAssignment walks assignments.assigned_to_id (the agent doing the
// work), assignments.parent_assignment_id in both directions (delegation), and
// mission_tasks.assignment_id in reverse (back up to the issue).
func (w *walker) expandAssignment(ctx context.Context, n Node) ([]neighbour, error) {
	var out []neighbour

	var assignedTo, parentID string
	err := w.db.QueryRowContext(ctx, `
		SELECT COALESCE(assigned_to_id,''), COALESCE(parent_assignment_id,'')
		FROM assignments
		WHERE id = ? AND workspace_id = ?`,
		n.Ref, w.workspaceID,
	).Scan(&assignedTo, &parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if assignedTo != "" {
		var id, name, slug, status string
		err := w.db.QueryRowContext(ctx, `
			SELECT id, COALESCE(name,''), COALESCE(slug,''), COALESCE(status,'')
			FROM agents
			WHERE id = ? AND workspace_id = ?`,
			assignedTo, w.workspaceID,
		).Scan(&id, &name, &slug, &status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, err
		default:
			an := agentNode(id, name, slug, status)
			out = append(out, neighbour{node: an, edge: Edge{From: an.ID, To: n.ID, Kind: EdgeExecutes}})
		}
	}

	if parentID != "" {
		if pn, ok, err := w.lookupAssignmentByID(ctx, parentID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, neighbour{node: pn, edge: Edge{From: pn.ID, To: n.ID, Kind: EdgeTriggers}})
		}
	}

	if err := w.collect(ctx, &out, `
		SELECT id, COALESCE(task,''), COALESCE(status,'')
		FROM assignments
		WHERE workspace_id = ? AND parent_assignment_id = ?
		ORDER BY created_at ASC, id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, task, status string
			if err := rows.Scan(&id, &task, &status); err != nil {
				return neighbour{}, err
			}
			to := assignmentNode(id, task, status)
			return neighbour{node: to, edge: Edge{From: n.ID, To: to.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	if err := w.collect(ctx, &out, `
		SELECT m.id, COALESCE(m.identifier,''), m.title, m.status
		FROM mission_tasks mt
		JOIN missions m ON m.id = mt.mission_id AND m.workspace_id = ?
		WHERE mt.assignment_id = ?
		ORDER BY m.id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		func(rows *sql.Rows) (neighbour, error) {
			var id, identifier, title, status string
			if err := rows.Scan(&id, &identifier, &title, &status); err != nil {
				return neighbour{}, err
			}
			from := issueNode(id, identifier, title, status)
			return neighbour{node: from, edge: Edge{From: from.ID, To: n.ID, Kind: EdgeTriggers}}, nil
		}); err != nil {
		return nil, err
	}

	return out, nil
}

// expandInbox resolves the polymorphic source_id back to a run, for the two
// kinds that can carry one. Every other kind is a leaf and the node already
// says why.
func (w *walker) expandInbox(ctx context.Context, n Node) ([]neighbour, error) {
	var kind, sourceID, payload string
	err := w.db.QueryRowContext(ctx, `
		SELECT kind, COALESCE(source_id,''), COALESCE(payload_json,'{}')
		FROM inbox_items
		WHERE id = ? AND workspace_id = ?`,
		n.Ref, w.workspaceID,
	).Scan(&kind, &sourceID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var runID string
	switch kind {
	case "waitpoint":
		err := w.db.QueryRowContext(ctx, `
			SELECT pipeline_run_id FROM pipeline_waitpoints
			WHERE token = ? AND workspace_id = ?`,
			sourceID, w.workspaceID,
		).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	case "failed_run":
		// Read the run id back out through SQLite rather than unmarshalling
		// in Go, so the extraction is byte-for-byte the same expression the
		// forward walk matches on.
		var extracted sql.NullString
		if err := w.db.QueryRowContext(ctx,
			`SELECT CASE WHEN json_valid(?) THEN json_extract(?, '$.run_id') END`,
			payload, payload).Scan(&extracted); err != nil {
			return nil, err
		}
		runID = extracted.String
	default:
		return nil, nil
	}
	if runID == "" {
		return nil, nil
	}

	rn, ok, err := w.lookupRunByID(ctx, runID)
	if err != nil || !ok {
		return nil, err
	}
	return []neighbour{{node: rn, edge: Edge{From: rn.ID, To: n.ID, Kind: EdgeProduces}}}, nil
}

// collect runs one neighbour query and appends what scan yields. Factored out
// so every expansion shares the same rows.Close/rows.Err discipline — a
// forgotten rows.Err() is how a truncated result set becomes a silently
// smaller graph.
func (w *walker) collect(ctx context.Context, out *[]neighbour, query string, args []any, scan func(*sql.Rows) (neighbour, error)) error {
	rows, err := w.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		nb, err := scan(rows)
		if err != nil {
			return err
		}
		*out = append(*out, nb)
	}
	return rows.Err()
}
