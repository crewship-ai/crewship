package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestAssignmentRefusalJournalUsesVerifiedSource(t *testing.T) {
	f := newChatIssueFixture(t)
	other := seedCrewRow(t, f.h.db, "refusal-target", f.workspace, "Other", "other")
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	writer := journal.NewWriter(f.h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h.SetJournal(writer)
	body := map[string]any{"workspace_id": f.workspace, "crew_id": other, "actor_agent_id": f.agent, "source_run_id": f.run, "target_slug": "absent-target", "task": "private task contents XLINK-test"}
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", jsonBody(body))
		ctx := context.WithValue(req.Context(), ctxInternalTokenWS, f.workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.crew)
		rr := httptest.NewRecorder()
		h.Create(rr, req.WithContext(ctx))
		return rr
	}
	body["source_run_id"] = "forged-source"
	if rr := post(); rr.Code != http.StatusForbidden {
		t.Fatalf("forged source: %d %s", rr.Code, rr.Body.String())
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='assignment.refused'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("forged source receipt: count=%d err=%v", count, err)
	}
	body["source_run_id"] = f.run
	if rr := post(); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "crews are not connected") {
		t.Fatalf("unlinked: %d %s", rr.Code, rr.Body.String())
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var workspace, crew, agent, actor, trace, payload string
	if err := f.h.db.QueryRow(`SELECT workspace_id,crew_id,agent_id,actor_id,trace_id,payload FROM journal_entries WHERE entry_type='assignment.refused'`).Scan(&workspace, &crew, &agent, &actor, &trace, &payload); err != nil {
		t.Fatal(err)
	}
	if workspace != f.workspace || crew != f.crew || agent != f.agent || actor != f.agent || trace != f.run {
		t.Fatalf("wrong attribution: %s %s %s %s %s", workspace, crew, agent, actor, trace)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatal(err)
	}
	if got["reason"] != "crews_not_connected" || got["http_status"] != float64(403) || got["source_crew_id"] != f.crew || got["target_crew_id"] != other || got["target_slug"] != "absent-target" {
		t.Fatalf("receipt: %v", got)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body["task"].(string))))
	if got["task_sha256"] != hash || strings.Contains(payload, "private task contents") {
		t.Fatalf("task disclosure or wrong digest: %s", payload)
	}
	execOrFatal(t, f.h.db, `INSERT INTO crew_connections(id,workspace_id,from_crew_id,to_crew_id,direction,status) VALUES('refusal-link',?,?,?,'bidirectional','active')`, f.workspace, f.crew, other)
	if rr := post(); rr.Code != http.StatusNotFound {
		t.Fatalf("linked target lookup: %d %s", rr.Code, rr.Body.String())
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='assignment.refused'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected exactly one receipt after linked request: count=%d err=%v", count, err)
	}
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied request admitted work: count=%d err=%v", count, err)
	}
}

func TestSidecarCannotForgeAssignmentRefusal(t *testing.T) {
	emitter := &emitRecorder{}
	router := newJournalTestRouter(emitter)
	body := map[string]any{"workspace_id": "ws1", "type": "assignment.refused", "summary": "claimed refusal", "payload": map[string]any{"reason": "crews_not_connected", "http_status": 403}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/journal/emit", jsonBody(body))
	rec := httptest.NewRecorder()
	router.handleSidecarEmit(rec, req)
	if rec.Code != http.StatusBadRequest || len(emitter.entries) != 0 {
		t.Fatalf("sidecar forged server receipt: status=%d emitted=%d", rec.Code, len(emitter.entries))
	}
}
