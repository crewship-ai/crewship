package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

// Transport projections are tested separately from the real Node/authority/CLI
// acceptance: this callback is not evidence of authorization or engine behavior.
func TestHumanDocumentHistoryClosedProjection(t *testing.T) {
	for _, tc := range []struct {
		name, checkpoint, response string
		upstream, want             int
	}{
		{"list", "", `{"checkpoints":[{"id":"pin-one","rev":1,"schema":3,"actor":"user:author","created_at":"2026-10-18T00:00:00Z","snapshot":"ENGINE_PRIVATE"}],"epoch":"ENGINE_PRIVATE"}`, 200, 200},
		{"empty list", "", `{"checkpoints":[]}`, 200, 200},
		{"null list", "", `{"checkpoints":null}`, 200, 502},
		{"duplicate pin", "", `{"checkpoints":[{"id":"pin-one","rev":1,"schema":3},{"id":"pin-one","rev":1,"schema":3}]}`, 200, 502},
		{"unsafe revision", "", `{"checkpoints":[{"id":"pin-one","rev":9007199254740992,"schema":3}]}`, 200, 502},
		{"selected", "pin-one", `{"id":"pin-one","rev":1,"schema":3,"content":{"type":"doc","content":[]},"snapshot":"ENGINE_PRIVATE","blocks":"ENGINE_PRIVATE","epoch":"ENGINE_PRIVATE"}`, 200, 200},
		{"wrong pin", "pin-one", `{"id":"other","rev":1,"schema":3,"content":{"type":"doc"}}`, 200, 502},
		{"missing body", "pin-one", `{"id":"pin-one","rev":1,"schema":3}`, 200, 502},
		{"non document", "pin-one", `{"id":"pin-one","rev":1,"schema":3,"content":{"type":"paragraph"}}`, 200, 502},
		{"selected schema 4", "pin-one", `{"id":"pin-one","rev":1,"schema":4,"content":{"type":"doc","content":[]}}`, 200, 200},
		{"selected schema 6", "pin-one", `{"id":"pin-one","rev":1,"schema":6,"content":{"type":"doc","content":[]}}`, 200, 200},
		{"schema 7", "pin-one", `{"id":"pin-one","rev":1,"schema":7,"content":{"type":"doc","content":[]}}`, 200, 409},
		{"future schema", "pin-one", `{"id":"pin-one","rev":1,"schema":99,"content":{"type":"doc","content":[{"type":"text","text":"ENGINE_PRIVATE"}]}}`, 200, 409},
		{"denied", "pin-one", `{"error":"ENGINE_PRIVATE"}`, 403, 403},
		{"unavailable", "", `{"error":"ENGINE_PRIVATE"}`, 503, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, user := humanDocumentTransportFixture(t)
			calls := 0
			h.documentCall = func(_ context.Context, workspace, bearer, page, operation string, body any) ([]byte, int, error) {
				calls++
				wantOperation := "checkpoints"
				if tc.checkpoint != "" {
					wantOperation = "checkpoint-read"
				}
				if workspace != ws || page != "human-doc-page" || bearer != "Bearer original-human-credential" || operation != wantOperation {
					t.Fatalf("history changed trusted request identity: %s %s", page, operation)
				}
				input, _ := json.Marshal(body)
				wantInput := `{}`
				if tc.checkpoint != "" {
					wantInput = `{"checkpoint":"pin-one"}`
				}
				if string(input) != wantInput {
					t.Fatalf("history request=%s", input)
				}
				return []byte(tc.response), tc.upstream, nil
			}
			r := humanDocumentRequest(t, "GET", "/history", ws, user, "")
			w := httptest.NewRecorder()
			if tc.checkpoint == "" {
				h.ListDocumentCheckpoints(w, r)
			} else {
				r.SetPathValue("checkpoint", tc.checkpoint)
				h.ReadDocumentCheckpoint(w, r)
			}
			if calls != 1 || w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("history status=%d calls=%d body=%s", w.Code, calls, w.Body)
			}
			if strings.Contains(w.Body.String(), "ENGINE_PRIVATE") {
				t.Fatal("history disclosed private engine fields")
			}
			if tc.checkpoint != "" && tc.want == 200 {
				var value map[string]any
				if json.Unmarshal(w.Body.Bytes(), &value) != nil || value["page_id"] != "human-doc-page" || len(value) != 5 {
					t.Fatalf("checkpoint projection=%s", w.Body)
				}
			}
		})
	}
}

func TestHumanDocumentHistoryRejectsUnboundedIdentityBeforeEngine(t *testing.T) {
	for _, id := range []string{"", "../other", strings.Repeat("x", 129)} {
		h, ws, user := humanDocumentTransportFixture(t)
		h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
			t.Fatal("invalid identity reached engine")
			return nil, 500, nil
		}
		r := humanDocumentRequest(t, "GET", "/history", ws, user, "")
		r.SetPathValue("checkpoint", id)
		w := httptest.NewRecorder()
		h.ReadDocumentCheckpoint(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid checkpoint status=%d", w.Code)
		}
	}
}

// When the automatic snapshots have used their half of the History budget, the
// list says so (and only then), so History can explain why no new ones appear.
func TestHumanDocumentHistoryReportsTheSessionLimit(t *testing.T) {
	h, ws, user := humanDocumentTransportFixture(t)
	h.documentStoreOptions = documents.Options{MaxHistoryBytes: 200}
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return []byte(`{"checkpoints":[]}`), 200, nil
	}
	// The flag re-runs the authority's human read check, which binds the exact session.
	if _, err := h.db.Exec(`INSERT INTO user_sessions(id,user_id,expires_at) VALUES('history-limit-session',?,?)`, user, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	request := func(session string) *http.Request {
		r := humanDocumentRequest(t, "GET", "/history", ws, user, "")
		return r.WithContext(withUser(r.Context(), &AuthUser{ID: user, Email: user + "@example.com", SessionID: session}))
	}
	list := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		h.ListDocumentCheckpoints(w, request("history-limit-session"))
		var value map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &value) != nil {
			t.Fatalf("list %d %s", w.Code, w.Body)
		}
		return value
	}
	if value := list(); len(value) != 1 {
		t.Fatalf("no document state: the list stays closed: %v", value)
	}
	k := documents.Key{WorkspaceID: ws, PageID: "human-doc-page"}
	allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + user, nil }
	store := documents.New(h.db, documents.Options{MaxHistoryBytes: 200})
	if _, err := store.Init(context.Background(), k, 1, []byte("baseline"), []byte(`[{"id":"one","hash":"h1"}]`), allow); err != nil {
		t.Fatal(err)
	}
	if value := list(); len(value) != 1 {
		t.Fatalf("History below its limit must not report one: %v", value)
	}
	// Fill the automatic half directly: the store test covers how pins get there.
	if _, err := h.db.Exec(`INSERT INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor) VALUES(?,?,'session-filled',1,1,?,'[]','user:x')`, ws, "human-doc-page", strings.Repeat("s", 95)); err != nil {
		t.Fatal(err)
	}
	if value := list(); value["history_limit_reached"] != true {
		t.Fatalf("full automatic History must be reported: %v", value)
	}
	// Without a live session the authority check refuses, and the list carries no flag.
	w := httptest.NewRecorder()
	h.ListDocumentCheckpoints(w, request("revoked-or-unknown"))
	var value map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &value) != nil || len(value) != 1 {
		t.Fatalf("unauthorized limit read leaked: %d %s", w.Code, w.Body)
	}
}
