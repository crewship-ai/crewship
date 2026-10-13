package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestQueryChatOriginIsDurableAndCannotBecomeAutonomous(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug,status) VALUES('query-origin-peer',?,?,'Peer','query-origin-peer','IDLE')`, f.workspace, f.crew)
	var fromSlug string
	if err := f.h.db.QueryRow(`SELECT slug FROM agents WHERE id=?`, f.agent).Scan(&fromSlug); err != nil {
		t.Fatal(err)
	}
	h := NewQueryHandler(f.h.db, nil, nil, "host", newTestLogger())
	host := NewInternalHandler(f.h.db, "host", newTestLogger())
	wireTestJournalForHandler(t, f.h.db, host)
	h.SetJournal(host.journal)
	body := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "chat_id": f.chat, "from_slug": fromSlug, "target_slug": "query-origin-peer", "question": "work for this user", "source_run_id": f.run}
	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
	// Admission reached the genuine CLI execution boundary. No executor was
	// installed in this test, so the child is durably failed rather than run.
	if rr.Code != 503 {
		t.Fatalf("query admission %d %s", rr.Code, rr.Body.String())
	}
	if err := host.journal.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var child, payload string
	if err := f.h.db.QueryRow(`SELECT trace_id,payload FROM journal_entries WHERE entry_type='run.started' AND agent_id='query-origin-peer'`).Scan(&child, &payload); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	origin, _ := p["chat_origin_id"].(string)
	if origin == "" {
		t.Fatalf("query lost human origin: %s", payload)
	}
	a, err := runauthority.ValidateOrigin(t.Context(), f.h.db, origin, "query-origin-peer", f.workspace)
	if err != nil || a.HumanAuthority == nil || a.HumanAuthority.UserID != f.user || a.ChatID != f.chat {
		t.Fatalf("peer authority %+v %v", a, err)
	}
	// Even a normally failed peer run must never resolve as an autonomous run.
	if _, err := runauthority.Resolve(t.Context(), f.h.db, child, "query-origin-peer", f.workspace); err == nil {
		t.Fatal("ended child accepted for mutation")
	}
	body["source_run_id"] = "forged-source"
	rr = httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
	if rr.Code != 403 {
		t.Fatalf("forged query source %d %s", rr.Code, rr.Body.String())
	}
	var count int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM peer_conversations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("peer rows %d %v", count, err)
	}
}
