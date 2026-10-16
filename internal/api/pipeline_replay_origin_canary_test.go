package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestReplayPrivateOriginGuestCanary(t *testing.T) {
	for _, source := range []string{"historical", "trusted", "issue", "conflicting"} {
		t.Run(source, func(t *testing.T) {
			h, db, owner, workspace := runsHandlerRig(t)
			h.SetRunner(&stubRunner{output: "PRIVATE_REPLAY_OUTPUT_CANARY"})
			runStore := pipeline.NewRunStore(db)
			h.SetRunStore(runStore)
			seedVersionedPipeline(t, db, workspace, "private-replay-recipe", "private-replay")
			seedRunRow(t, db, workspace, "private-replay-recipe", "private-replay", "private-replay-source", "completed")
			execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('replay-crew',?,'Crew','replay-crew')`, workspace)
			seedAgentRow(t, db, "replay-agent", workspace, "replay-crew", "Agent", "replay-agent", "AGENT")
			execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,title) VALUES('replay-chat',?,'replay-agent',?,'private','Private DM')`, workspace, owner)
			authority, err := chataudience.CaptureTrusted(t.Context(), db, "replay-chat", owner, nil)
			if err != nil || authority == nil {
				t.Fatalf("capture source receipt: %v", err)
			}
			receipt, err := json.Marshal(authority)
			if err != nil {
				t.Fatal(err)
			}
			execOrFatal(t, db, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('private-replay-origin',?,'private-replay-root','replay-chat','replay-message',?,datetime('now'))`, workspace, string(receipt))
			if source == "historical" || source == "conflicting" {
				execOrFatal(t, db, `UPDATE pipeline_runs SET chat_origin_id='private-replay-origin' WHERE id='private-replay-source'`)
			}
			if source == "issue" {
				execOrFatal(t, db, `UPDATE pipeline_runs SET triggered_via='issue' WHERE id='private-replay-source'`)
			}
			req := withWorkspaceUser(httptest.NewRequest("POST", "/replay", strings.NewReader(`{}`)), owner, workspace, "OWNER")
			if source == "trusted" {
				req = req.WithContext(runauthority.WithOriginID(req.Context(), "private-replay-origin"))
			}
			if source == "conflicting" {
				req = req.WithContext(runauthority.WithOriginID(req.Context(), "different-private-origin"))
			}
			req.SetPathValue("runId", "private-replay-source")
			rr := httptest.NewRecorder()
			h.ReplayRun(rr, req)
			if source == "conflicting" {
				if rr.Code != http.StatusConflict {
					t.Fatalf("conflicting origins: %d %s", rr.Code, rr.Body.String())
				}
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("conflicting replay admitted: count=%d err=%v", count, err)
				}
				return
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("replay: %d %s", rr.Code, rr.Body.String())
			}
			var replay struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &replay); err != nil || replay.RunID == "" {
				t.Fatalf("replay response: %s", rr.Body.String())
			}
			rec, err := runStore.Get(t.Context(), replay.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if source != "issue" && rec.ChatOriginID != "private-replay-origin" {
				t.Fatalf("lost %s origin: %q", source, rec.ChatOriginID)
			}
			if source == "issue" && rec.TriggeredVia != pipeline.TriggeredViaIssue {
				t.Fatalf("lost issue source: %s", rec.TriggeredVia)
			}
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('replay-guest','replay-guest@example.test')`)
			execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('replay-guest-member',?,'replay-guest','VIEWER')`, workspace)
			store := access.Store{DB: db}
			member, err := store.Membership(t.Context(), "replay-guest", workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Replace(t.Context(), owner, "replay-guest", workspace, "restricted", member, []access.Right{{Kind: "routine", ID: "private-replay-recipe", Operation: "view"}, {Kind: "routine", ID: "private-replay-recipe", Operation: "results"}}); err != nil {
				t.Fatal(err)
			}
			router, err := NewRouter(db, "private-replay-canary-secret-long-enough", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			token := mintTokenFor(t, db, "replay-guest", strings.Repeat("r", 48))
			base := "/api/v1/workspaces/" + workspace + "/restricted-routine-library/private-replay-recipe/runs"
			for _, path := range []string{base, base + "/" + replay.RunID} {
				guest := httptest.NewRequest("GET", path, nil)
				guest.Header.Set("Authorization", "Bearer "+token)
				guest.Header.Set("X-Workspace-ID", workspace)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, guest)
				want := 404
				if path == base {
					want = 200
				}
				if response.Code != want {
					t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
				}
				if strings.Contains(response.Body.String(), replay.RunID) || strings.Contains(response.Body.String(), "PRIVATE_REPLAY_OUTPUT_CANARY") {
					t.Fatalf("replay leaked: %s", response.Body.String())
				}
			}
		})
	}
}
