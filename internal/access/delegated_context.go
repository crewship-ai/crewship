package access

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ImportDelegatedContext is a trusted host transfer, not a text ingestion API.
// It copies only classified sources into the exact admitted child, preserving
// their immutable identities and current authority through completion.
func (s Store) ImportDelegatedContext(ctx context.Context, parentHandle, childHandle string, sources []string) (ContextEntry, error) {
	return s.importDelegatedContext(ctx, parentHandle, childHandle, sources, false)
}

// ImportWorkflowContinuation preserves foreign leaf provenance when returning
// to the original frozen agent. It accepts only a durable continuation child.
func (s Store) ImportWorkflowContinuation(ctx context.Context, parentHandle, childHandle string, sources []string) (ContextEntry, error) {
	return s.importDelegatedContext(ctx, parentHandle, childHandle, sources, true)
}

func (s Store) importDelegatedContext(ctx context.Context, parentHandle, childHandle string, sources []string, continuation bool) (ContextEntry, error) {
	if s.DB == nil || len(sources) == 0 || len(sources) > 64 {
		return ContextEntry{}, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ContextEntry{}, err
	}
	defer tx.Rollback()
	parent, err := resolve(ctx, tx, digest(parentHandle), true, map[string]bool{})
	if err != nil {
		return ContextEntry{}, err
	}
	child, err := resolve(ctx, tx, digest(childHandle), true, map[string]bool{})
	if err != nil {
		return ContextEntry{}, err
	}
	returnAllowed := continuation && child.WorkflowContinuation && workflowContinuationSlot(ctx, tx, parent, child.Agent) == nil
	if (continuation && !returnAllowed) || child.Parent != parent.ID || child.Scope != parent.Scope || child.ContextAudience != "" || parent.ContextAudience != "" || child.AdmissionOperation != "run" || parent.AdmissionOperation != "run" || !subset(child.Rights, parent.Rights) || (!slices.Contains(parent.Rights, Right{"agent", child.Agent, "delegate"}) && !returnAllowed) {
		return ContextEntry{}, ErrDenied
	}
	entries := make([]ContextEntry, 0, len(sources))
	origins := make([]string, 0, len(sources))
	unique := map[string]bool{}
	for _, id := range sources {
		if unique[id] {
			return ContextEntry{}, ErrDenied
		}
		unique[id] = true
		var originID string
		if tx.QueryRowContext(ctx, `SELECT attempt_id FROM access_context WHERE id=? AND scope=?`, id, child.Scope).Scan(&originID) != nil {
			return ContextEntry{}, ErrDenied
		}
		origin, err := resolveState(ctx, tx, originID, false, map[string]bool{}, true)
		if err != nil || origin.Generation >= child.Generation || !subset(origin.Rights, child.Rights) || (origin.Agent != parent.Agent && origin.Parent != parent.ID) {
			return ContextEntry{}, ErrDenied
		}
		reader := child
		reader.Agent = origin.Agent
		entry, err := readContext(ctx, tx, reader, id, map[string]bool{})
		if err != nil {
			return ContextEntry{}, err
		}
		entries = append(entries, entry)
		origins = append(origins, originID)
	}
	content, err := json.Marshal(struct {
		Delegated []ContextEntry `json:"delegatedSources"`
	}{entries})
	if err != nil || len(content) > 32768 {
		return ContextEntry{}, ErrDenied
	}
	raw, _ := json.Marshal(sources)
	entry := ContextEntry{ID: randomID(), Kind: ContextSummary, Role: "derived", Content: string(content), Sources: slices.Clone(sources), CreatedAt: tsformat.Format(time.Now())}
	if _, err = tx.ExecContext(ctx, `INSERT INTO access_context(id,attempt_id,scope,agent_id,kind,role,content,sources,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, entry.ID, child.ID, child.Scope, child.Agent, entry.Kind, entry.Role, entry.Content, string(raw), entry.CreatedAt); err != nil {
		return ContextEntry{}, err
	}
	for i, id := range sources {
		if _, err = tx.ExecContext(ctx, `INSERT INTO access_context_delegations(entry_id,source_context_id,parent_attempt_id,child_attempt_id) VALUES(?,?,?,?)`, entry.ID, id, parent.ID, child.ID); err != nil {
			return ContextEntry{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO access_context_dependencies(attempt_id,context_id,source_attempt_id,scope) VALUES(?,?,?,?) ON CONFLICT(attempt_id,context_id) DO NOTHING`, child.ID, id, origins[i], child.Scope); err != nil {
			return ContextEntry{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return ContextEntry{}, err
	}
	if _, err = s.Resolve(ctx, childHandle); err != nil {
		return ContextEntry{}, err
	}
	return entry, nil
}

func readDelegatedContextSource(ctx context.Context, q queryer, reader Attempt, entryID, sourceID string, seen map[string]bool) (ContextEntry, error) {
	var parentID, childID, originID string
	if q.QueryRowContext(ctx, `SELECT d.parent_attempt_id,d.child_attempt_id,c.attempt_id FROM access_context_delegations d JOIN access_context c ON c.id=d.source_context_id WHERE d.entry_id=? AND d.source_context_id=?`, entryID, sourceID).Scan(&parentID, &childID, &originID) != nil {
		return ContextEntry{}, ErrDenied
	}
	parent, err := resolveState(ctx, q, parentID, false, map[string]bool{}, true)
	if err != nil {
		return ContextEntry{}, err
	}
	child, err := resolveState(ctx, q, childID, false, map[string]bool{}, true)
	if err != nil {
		return ContextEntry{}, err
	}
	origin, err := resolveState(ctx, q, originID, false, map[string]bool{}, true)
	if err != nil {
		return ContextEntry{}, err
	}
	returnAllowed := child.WorkflowContinuation && workflowContinuationSlot(ctx, q, parent, child.Agent) == nil
	if child.Parent != parent.ID || child.Scope != reader.Scope || child.Agent != reader.Agent || child.ContextAudience != "" || origin.Scope != reader.Scope || origin.Generation >= child.Generation || (origin.Agent != parent.Agent && origin.Parent != parent.ID) || !subset(origin.Rights, reader.Rights) || !subset(child.Rights, reader.Rights) || (!slices.Contains(parent.Rights, Right{"agent", child.Agent, "delegate"}) && !returnAllowed) {
		return ContextEntry{}, ErrDenied
	}
	sourceReader := reader
	sourceReader.Agent = origin.Agent
	return readContext(ctx, q, sourceReader, sourceID, seen)
}
