package api

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestCrewGrantDetailRechecksAuthorityBeforeReadingNewAgents(t *testing.T) {
	for _, change := range []string{"revoke", "issuer_role_loss", "retire"} {
		t.Run(change, func(t *testing.T) {
			mdb := testutil.MigratedDB(t)
			db := mdb.DB
			owner := seedTestUser(t, db)
			workspace := seedTestWorkspace(t, db, owner)
			execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('racing-crew',?,'Shared','shared')`, workspace)
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('racing-guest','racing-guest@fixture.invalid')`)
			execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('racing-guest',?,'racing-guest','VIEWER','restricted')`, workspace)
			store := access.Store{DB: db}
			member, err := store.Membership(t.Context(), "racing-guest", workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.Replace(t.Context(), owner, "racing-guest", workspace, "restricted", member, []access.Right{{Kind: "crew", ID: "racing-crew", Operation: "view"}}); err != nil {
				t.Fatal(err)
			}
			counted, counter := testutil.CountingSQLDB(t, mdb.Path(), 1)
			router, err := NewRouter(counted, "crew-racing-test-secret-longer-than-32", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			token := mintTokenFor(t, db, "racing-guest", "racing-guest-session")
			var once sync.Once
			changed := false
			counter.BeforeQuery(func(query string) {
				if !strings.Contains(query, "agent_role") || !strings.Contains(query, "agents") {
					return
				}
				once.Do(func() {
					// The first detail read saw a live root. No agent exists yet. Commit
					// authority loss and then a brand-new private identity before read two.
					switch change {
					case "revoke":
						execOrFatal(t, db, `DELETE FROM access_grants WHERE member_id='racing-guest'`)
					case "issuer_role_loss":
						execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, workspace)
					case "retire":
						execOrFatal(t, db, `UPDATE crews SET deleted_at=datetime('now') WHERE id='racing-crew'`)
					}
					seedAgentRow(t, db, "never-shared-agent", workspace, "racing-crew", "NEVER-SHARED-PRIVATE-IDENTITY", "never-shared", "AGENT")
					changed = true
				})
			})
			defer counter.BeforeQuery(nil)
			req := httptest.NewRequest("GET", "/api/v1/crews/racing-crew", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Workspace-ID", workspace)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if !changed {
				t.Fatal("authority interleaving did not execute")
			}
			if response.Code != 404 || strings.Contains(response.Body.String(), "never-shared") || strings.Contains(response.Body.String(), "NEVER-SHARED") {
				t.Fatalf("post-revocation data escaped: HTTP%d %s", response.Code, response.Body.String())
			}
		})
	}
}
