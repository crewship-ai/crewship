package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestLoadRunAgentSpans_GroupsByStepOrderedBySeq(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := &PipelineHandler{
		db:     db,
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}

	runID := "run_subspans_1"

	insert := func(id, stepID string, seq int, kind, name, status string, attrs map[string]string) {
		payload := map[string]any{
			"run_id":      runID,
			"step_id":     stepID,
			"seq":         seq,
			"kind":        kind,
			"name":        name,
			"detail":      name + "-detail",
			"started_at":  "2026-06-30T09:00:00Z",
			"duration_ms": 10 * (seq + 1),
			"status":      status,
		}
		if attrs != nil {
			payload["attributes"] = attrs
		}
		raw, _ := json.Marshal(payload)
		if _, err := db.Exec(`INSERT INTO journal_entries
			(id, workspace_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
			VALUES (?, ?, '2026-06-30T09:00:00.000Z', ?, 'info', 'normal', 'orchestrator', ?, 'sub-span', ?, '{}', ?)`,
			id, wsID, string(journal.EntryRunAgentSpan), runID, string(raw), runID); err != nil {
			t.Fatalf("seed sub-span %s: %v", id, err)
		}
	}

	// Insert out of seq order across two steps; loader must group + sort.
	insert("j1", "stepA", 1, "write", "Write", "ok", map[string]string{"artifact_path": "/out/x"})
	insert("j2", "stepA", 0, "bash", "Bash", "ok", nil)
	insert("j3", "stepB", 0, "mcp_tool", "save_routine", "error", map[string]string{"tool": "mcp__crewship-routines__save_routine"})

	spans := h.loadRunAgentSpans(context.Background(), wsID, runID)

	if len(spans) != 2 {
		t.Fatalf("expected 2 step groups, got %d (%v)", len(spans), spans)
	}
	a := spans["stepA"]
	if len(a) != 2 {
		t.Fatalf("stepA: expected 2 spans, got %d", len(a))
	}
	// Sorted by seq ascending: Bash (0) then Write (1).
	if a[0]["name"] != "Bash" || a[1]["name"] != "Write" {
		t.Errorf("stepA order = %v, %v; want Bash, Write", a[0]["name"], a[1]["name"])
	}
	if a[0]["kind"] != "bash" {
		t.Errorf("stepA[0].kind = %v", a[0]["kind"])
	}
	// Write span carries its attributes through.
	if attrs, ok := a[1]["attributes"].(map[string]any); !ok || attrs["artifact_path"] != "/out/x" {
		t.Errorf("stepA[1].attributes = %v", a[1]["attributes"])
	}

	b := spans["stepB"]
	if len(b) != 1 || b[0]["status"] != "error" {
		t.Errorf("stepB = %v", b)
	}
}

func TestLoadRunAgentSpans_EmptyForRunWithNone(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := &PipelineHandler{
		db:     db,
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}

	spans := h.loadRunAgentSpans(context.Background(), wsID, "run_with_no_spans")
	if spans == nil {
		t.Fatal("expected non-nil empty map, got nil")
	}
	if len(spans) != 0 {
		t.Errorf("expected empty map, got %v", spans)
	}
	// Must serialize to a JSON object ({}), not null, so the frontend's
	// per-step lookup never NPEs.
	raw, _ := json.Marshal(spans)
	if string(raw) != "{}" {
		t.Errorf("serialized empty sub_spans = %s, want {}", raw)
	}
}
