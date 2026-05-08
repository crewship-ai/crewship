package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// emitRecorder is a test-only journal.Emitter that captures entries so the
// handler's mapping from wire format → journal.Entry can be asserted.
type emitRecorder struct {
	entries []journal.Entry
}

func (r *emitRecorder) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.entries = append(r.entries, e)
	if e.ID == "" {
		return "rec-id", nil
	}
	return e.ID, nil
}
func (r *emitRecorder) Flush(_ context.Context) error { return nil }

// newJournalTestRouter wires a Router with just enough surface area to
// test the sidecar-emit endpoint. We deliberately skip the full
// NewRouter() construction because it requires a JWT validator and
// pre-existing DB; the handler under test only reaches into
// r.Journal() and r.logger.
func newJournalTestRouter(em journal.Emitter) *Router {
	return &Router{
		logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		journal: em,
	}
}

func TestHandleSidecarEmit_AllowsNetworkEgress(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws1",
		"crew_id":      "c1",
		"agent_id":     "a1",
		"type":         "network.egress",
		"summary":      "GET api.anthropic.com → 200",
		"payload":      map[string]any{"host": "api.anthropic.com", "method": "GET", "status_code": 200},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	if len(em.entries) != 1 {
		t.Fatalf("expected 1 entry emitted, got %d", len(em.entries))
	}
	got := em.entries[0]
	if got.Type != journal.EntryNetworkEgress {
		t.Errorf("type = %s, want network.egress", got.Type)
	}
	if got.ActorType != journal.ActorSidecar {
		t.Errorf("actor = %s, want sidecar", got.ActorType)
	}
	if got.WorkspaceID != "ws1" {
		t.Errorf("workspace_id = %s", got.WorkspaceID)
	}
}

func TestHandleSidecarEmit_RejectsDisallowedType(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws1",
		// The sidecar should never be able to fabricate mission / approval events.
		"type":    "approval.granted",
		"summary": "pwned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if len(em.entries) != 0 {
		t.Errorf("disallowed type should not reach emitter; got %d entries", len(em.entries))
	}
}

func TestHandleSidecarEmit_RejectsMissingWorkspace(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	body, _ := json.Marshal(map[string]any{
		"type":    "network.egress",
		"summary": "no workspace",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSidecarEmit_RejectsEmptySummary(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws1",
		"type":         "file.written",
		"summary":      "",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleSidecarEmit_BodyTooBig pins the 64 KiB MaxBytesReader cap.
// The cap exists to make exfiltration through sidecar-emitted journal
// rows expensive; a future regression that drops the cap would let an
// attacker stuff a few MB of stolen data into a single payload field.
func TestHandleSidecarEmit_BodyTooBig(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	huge := make(map[string]any, 100)
	for i := 0; i < 200; i++ {
		// 1 KiB string × 200 → ~200 KiB payload, comfortably above the cap.
		huge["k"+string(rune('A'+(i%26)))+string(rune('A'+((i/26)%26)))] = bytes.Repeat([]byte("x"), 1024)
	}
	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws1",
		"type":         "network.egress",
		"summary":      "huge",
		"payload":      huge,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body too big); body = %s", rec.Code, rec.Body.String())
	}
	if len(em.entries) != 0 {
		t.Errorf("oversized body must not reach emitter; got %d entries", len(em.entries))
	}
}

// TestHandleSidecarEmit_RejectsUnknownTypeAfterTrim catches the case
// where the sidecar tries to sneak through a disallowed type with
// surrounding whitespace — trim-then-allowlist must still reject.
func TestHandleSidecarEmit_RejectsUnknownTypeAfterTrim(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws1",
		"type":         "  approval.granted  ",
		"summary":      "trimmed",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleSidecarEmit_AllowsFileWritten covers the second allowlisted
// type so the allowlist is exercised on both arms.
func TestHandleSidecarEmit_AllowsFileWritten(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws1",
		"crew_id":      "c1",
		"agent_id":     "a1",
		"type":         "file.written",
		"summary":      "wrote /workspace/.env",
		"payload":      map[string]any{"path": "/workspace/.env"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if len(em.entries) != 1 || em.entries[0].Type != journal.EntryFileWritten {
		t.Errorf("emitted entry: %+v", em.entries)
	}
}

func TestHandleSidecarEmit_RejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	em := &emitRecorder{}
	r := newJournalTestRouter(em)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit",
		bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()

	r.handleSidecarEmit(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
