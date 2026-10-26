package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestCrewGrantCLIContextDeduplicatesAuthoritySources(t *testing.T) {
	for _, mode := range []string{"inherited", "direct", "overlap", "view"} {
		t.Run(mode, func(t *testing.T) {
			db := testutil.MigratedDB(t).DB
			owner := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, owner)
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('cli-guest','cli-guest@test.invalid'),('cli-ungranted','cli-ungranted@test.invalid')`)
			execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('cli-guest',?,'cli-guest','VIEWER','restricted'),('cli-ungranted',?,'cli-ungranted','VIEWER','restricted')`, ws, ws)
			execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('cli-crew',?,'CLI Crew','cli-crew')`, ws)
			seedAgentRow(t, db, "cli-agent", ws, "cli-crew", "CLI Agent", "cli-agent", "AGENT")
			store := access.Store{DB: db}
			m, err := store.Membership(t.Context(), "cli-guest", ws)
			if err != nil {
				t.Fatal(err)
			}
			rights := []access.Right{{Kind: "crew", ID: "cli-crew", Operation: "use"}}
			if mode == "direct" {
				rights = []access.Right{{Kind: "agent", ID: "cli-agent", Operation: "run"}}
			} else if mode == "view" {
				rights[0].Operation = "view"
			} else if mode == "overlap" {
				rights = append(rights, access.Right{Kind: "agent", ID: "cli-agent", Operation: "run"})
			}
			if _, err = store.Replace(t.Context(), owner, "cli-guest", ws, "restricted", m, rights); err != nil {
				t.Fatal(err)
			}
			router := &Router{db: db}
			for _, user := range []string{"cli-guest", "cli-ungranted"} {
				req := crewGrantRequest(user, ws, "POST", "/api/v1/agents/cli-agent/cli-context", []byte(`{}`))
				req.SetPathValue("agentId", "cli-agent")
				rr := httptest.NewRecorder()
				router.createRestrictedCLIContext(rr, req)
				if user == "cli-ungranted" || mode == "view" {
					if rr.Code != 404 {
						t.Fatalf("ungranted context=%d", rr.Code)
					}
					continue
				}
				if rr.Code != 201 {
					t.Fatalf("context=%d: %s", rr.Code, rr.Body.String())
				}
				var result restrictedCLIContextResponse
				if err = json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				var count int
				if err = db.QueryRow(`SELECT count(*) FROM chats WHERE id=? AND workspace_id=? AND agent_id='cli-agent' AND created_by='cli-guest' AND visibility='private' AND origin='CLI'`, result.ID, ws).Scan(&count); err != nil || count != 1 {
					t.Fatal("private CLI context", count, err)
				}
			}
			foreign := crewGrantRequest("cli-guest", "foreign-workspace", "POST", "/api/v1/agents/cli-agent/cli-context", []byte(`{}`))
			foreign.SetPathValue("agentId", "cli-agent")
			denied := httptest.NewRecorder()
			router.createRestrictedCLIContext(denied, foreign)
			if denied.Code != 404 {
				t.Fatalf("foreign context=%d", denied.Code)
			}
			want := 1
			if mode == "view" {
				want = 0
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM chats`).Scan(&count); err != nil || count != want {
				t.Fatal("unexpected chat side effects", count, err)
			}
		})
	}
}
