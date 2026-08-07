package chain

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/automation"
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

func runNode(id, pipelineSlug, status string, chainDepth int) Node {
	return Node{
		ID:         nodeID(KindRun, id),
		Kind:       KindRun,
		Ref:        id,
		Key:        pipelineSlug,
		Label:      pipelineSlug,
		Status:     status,
		ChainDepth: chainDepth,
	}
}

// automationNode carries what an automation card draws: the rule's name as the
// label, the event that arms it as the human key, and whether it is live as the
// status.
//
// Status is the literal "enabled"/"disabled" rather than the raw INTEGER
// column because Node.Status is a string every other kind fills with a row
// status, and because the client derives its enabled flag by comparing against
// "disabled" — a 0/1 here would silently read as enabled.
func automationNode(id, name, eventType string, enabled bool) Node {
	status := "disabled"
	if enabled {
		status = "enabled"
	}
	label := name
	if label == "" {
		label = id
	}
	return Node{
		ID:     nodeID(KindAutomation, id),
		Kind:   KindAutomation,
		Ref:    id,
		Key:    eventType,
		Label:  label,
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
//
// An automation is anchorable by id: "what is this rule wired to, and what has
// it actually done" is the question its author asks, and the rule is the only
// anchor that answers it.
func (w *walker) resolveAnchor(ctx context.Context, anchor string) (Node, error) {
	lookups := []func(context.Context, string) (Node, bool, error){
		w.lookupIssueByIdentifier,
		w.lookupRunByID,
		w.lookupAutomationByID,
		w.lookupIssueByID,
		w.lookupRoutineByID,
		w.lookupRoutineBySlug,
		w.lookupAssignmentByID,
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
	var chainDepth int
	err := w.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(pipeline_slug,''), COALESCE(status,''), COALESCE(chain_depth, 0)
		FROM pipeline_runs
		WHERE workspace_id = ? AND id = ?`,
		w.workspaceID, anchor,
	).Scan(&id, &slug, &status, &chainDepth)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return runNode(id, slug, status, chainDepth), true, nil
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

// lookupAutomationByID resolves an automations.id inside this workspace.
//
// It goes through automation.Store rather than hand-rolling the SELECT, and
// that is a correctness choice rather than a tidiness one: the store owns what
// "visible" means for a rule (in this workspace AND deleted_at IS NULL) and
// decodes action_config_json into the Action this walk needs. A second copy of
// that predicate here is exactly how a soft-deleted rule ends up drawn in the
// topology while every other surface reports it gone.
//
// It returns the decoded row alongside the node, so expandAutomation can reuse
// the already-parsed routine_slug instead of re-querying for it.
func (w *walker) lookupAutomation(ctx context.Context, id string) (automation.Automation, Node, bool, error) {
	a, err := automation.NewStore(w.db).Get(ctx, w.workspaceID, id)
	if errors.Is(err, automation.ErrNotFound) {
		return automation.Automation{}, Node{}, false, nil
	}
	if err != nil {
		return automation.Automation{}, Node{}, false, err
	}
	return a, automationNode(a.ID, a.Name, a.EventType, a.Enabled), true, nil
}

func (w *walker) lookupAutomationByID(ctx context.Context, anchor string) (Node, bool, error) {
	_, n, ok, err := w.lookupAutomation(ctx, anchor)
	return n, ok, err
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
		SELECT r.id, COALESCE(r.pipeline_slug,''), COALESCE(r.status,''), COALESCE(r.chain_depth, 0)
		FROM missions m
		JOIN pipeline_runs r
		  ON r.triggered_by_id = m.identifier
		 AND r.workspace_id = m.workspace_id
		WHERE m.id = ? AND m.workspace_id = ? AND r.triggered_via = 'issue'
		ORDER BY r.started_at DESC, r.id ASC
		LIMIT ?`,
		[]any{n.Ref, w.workspaceID, w.fanOutLimit()},
		w.scanRunNeighbour(n.ID, EdgeTriggers)); err != nil {
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
		SELECT `+runColumns+`
		FROM pipeline_runs
		WHERE workspace_id = ? AND pipeline_id = ?
		ORDER BY started_at DESC, id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		w.scanRunNeighbour(n.ID, EdgeRuns)); err != nil {
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
		// The origin of a composed chain. triggered_by_id is the automations.id
		// here, which makes this the one exact, non-inferred link from a run
		// back to the RULE that started it — without it a topology can draw
		// "routine -> run -> agent" and never say why any of it began.
		//
		// A rule that has since been soft-deleted does not resolve, so the run
		// simply has no parent rather than gaining a phantom one. The run row
		// keeps triggered_via='automation', so the fact that a rule started it
		// is still on the record even when the rule itself is no longer
		// readable.
		if an, ok, err := w.lookupAutomationByID(ctx, triggeredByID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, neighbour{node: an, edge: Edge{From: an.ID, To: n.ID, Kind: EdgeTriggers}})
		}
	}

	// Nested runs this run started.
	if err := w.collect(ctx, &out, `
		SELECT `+runColumns+`
		FROM pipeline_runs
		WHERE workspace_id = ? AND triggered_via = 'call_pipeline' AND triggered_by_id = ?
		ORDER BY started_at ASC, id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		w.scanRunNeighbour(n.ID, EdgeTriggers)); err != nil {
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

// runColumns is the run projection, in the order scanRunNeighbour reads it.
// One constant so a column added to the run node cannot land on some discovery
// paths and silently miss others — which is exactly how chain_depth would have
// ended up set on an anchored run and zero on the same run reached from its
// routine.
//
// Two queries cannot use it and spell the same list out: expandIssue needs the
// `r.` table alias, and lookupRunByID scans a single row. Both must stay in
// step with this list; scanRunNeighbour's Scan is what fails loudly if they
// drift.
const runColumns = `id, COALESCE(pipeline_slug,''), COALESCE(status,''), COALESCE(chain_depth, 0)`

func (w *walker) scanRunNeighbour(fromID string, kind EdgeKind) func(*sql.Rows) (neighbour, error) {
	return func(rows *sql.Rows) (neighbour, error) {
		var id, slug, status string
		var chainDepth int
		if err := rows.Scan(&id, &slug, &status, &chainDepth); err != nil {
			return neighbour{}, err
		}
		to := runNode(id, slug, status, chainDepth)
		return neighbour{node: to, edge: Edge{From: fromID, To: to.ID, Kind: kind}}, nil
	}
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

// expandAutomation walks the two things a rule is connected to: the routine it
// is aimed at (action_config_json -> routine_slug) and the runs it has actually
// caused (pipeline_runs.triggered_via='automation').
//
// # Why a routine does NOT expand back to the rules that name it
//
// These two links look symmetrical and are not, and the difference is the
// whole reason this walk is trustworthy.
//
// A run's triggered_by_id is a RECORD: that run exists because that rule fired.
// A rule's routine_slug is a STANDING INTENT: it says where the rule is aimed,
// not that it ever went off. One routine can be named by unboundedly many
// rules, and none of them need ever have fired.
//
// So the direction is decided by which end the reader anchored on, because the
// anchor IS the question:
//
//   - Anchored on the RULE, the rule is the subject. "It is aimed at `triage`
//     and has caused nothing" is the correct, complete answer, and the absence
//     of run nodes is itself the finding. Config is what was asked for.
//
//   - Anchored on a routine or a run, a rule is being offered as an
//     EXPLANATION. Fanning out to every rule that names the routine would draw
//     rules that did not fire with the identical `triggers` edge as the one
//     that did — and the reader has no way to tell them apart. A graph titled
//     "how this happened" that lists four candidate causes for a run started by
//     hand is not an incomplete answer, it is a wrong one that looks
//     authoritative. That is the failure mode this package's whole design rule
//     exists to avoid.
//
// The rules that DID fire are not lost by this: they stay reachable from the
// routine THROUGH THE RUNS THEY CAUSED (routine -> run -> automation), which is
// precisely the evidence that they fired. No extra query buys that reachability
// — it falls out of expandRun. A rule earns its place in the graph by having
// acted, and the run is the proof.
//
// One honest caveat: expanding a rule reaches its routine, and expanding that
// routine reaches every run of it, including runs this rule did not cause. The
// edge kinds stay truthful (`triggers` vs `runs`) but a reader composing them
// could over-read. That is a pre-existing property of the graph — an issue
// bound to a routine already reaches that routine's cron runs the same way —
// not something automations introduce, so it is documented rather than
// special-cased here.
func (w *walker) expandAutomation(ctx context.Context, n Node) ([]neighbour, error) {
	a, _, ok, err := w.lookupAutomation(ctx, n.Ref)
	if err != nil || !ok {
		return nil, err
	}

	var out []neighbour

	// The rule's target. Resolved by slug inside this workspace — the same
	// join the registry uses to arm the rule, so the chain shows the routine
	// that would actually fire rather than one that merely shares a name.
	//
	// A slug that does not resolve (routine renamed or deleted) yields no edge
	// rather than an error: the rule is still readable, and the missing routine
	// is visible as its absence, which is usually the thing the author is
	// debugging.
	if a.Action.RoutineSlug != "" {
		if rn, found, err := w.lookupRoutine(ctx, `slug = ?`, a.Action.RoutineSlug); err != nil {
			return nil, err
		} else if found {
			out = append(out, neighbour{node: rn, edge: Edge{From: n.ID, To: rn.ID, Kind: EdgeTriggers}})
		}
	}

	// The runs this rule actually caused. Not filtered on enabled: switching a
	// rule off stops it firing, it does not unmake the runs it already started,
	// and erasing them would make disabling a rule retroactively rewrite
	// history.
	if err := w.collect(ctx, &out, `
		SELECT `+runColumns+`
		FROM pipeline_runs
		WHERE workspace_id = ? AND triggered_via = 'automation' AND triggered_by_id = ?
		ORDER BY started_at DESC, id ASC
		LIMIT ?`,
		[]any{w.workspaceID, n.Ref, w.fanOutLimit()},
		w.scanRunNeighbour(n.ID, EdgeTriggers)); err != nil {
		return nil, err
	}

	return out, nil
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
