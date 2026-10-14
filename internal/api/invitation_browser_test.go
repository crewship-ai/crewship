//go:build linux && (invitation_browser || conformance)

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// A test-owned Router and migrated synthetic DB serve the production static
// export. No shared instance, provider, containers or fake authentication.
func TestInvitationBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_INVITATION_BROWSER") != "1" {
		t.Fatal("explicit browser acceptance requires CREWSHIP_INVITATION_BROWSER=1, current production export and Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out/index.html")); err != nil {
		t.Fatal("build current production export first", err)
	}
	db := setupTestDB(t)
	inviter, workspace := seedInviter(t, db)
	const password = "Synthetic-Invitation-2026!"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE users SET hashed_password=?,onboarding_completed=1 WHERE id=?`, string(hash), inviter)
	execOrFatal(t, db, `INSERT INTO users(id,email,full_name,hashed_password,onboarding_completed) VALUES('browser-existing','existing@example.test','Existing invited reader',?,1)`, string(hash))
	seedInvitation(t, db, "browser-new", workspace, inviter, "new-reader@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
	seedInvitation(t, db, "browser-existing", workspace, inviter, "existing@example.test", "VIEWER", time.Now().Add(time.Hour), nil)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('invitation-browser-crew',?,'Shared crew','invitation-browser-crew')`, workspace)
	seedAgentRow(t, db, "invitation-browser-agent", workspace, "invitation-browser-crew", "Shared agent", "invitation-browser-agent", "AGENT")
	execOrFatal(t, db, `UPDATE workspace_invitations SET access_mode='restricted',rights_json='[{"kind":"agent","id":"invitation-browser-agent","operation":"discover"}]' WHERE id IN ('browser-new','browser-existing')`)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('existing-own-workspace','Existing own','existing-own-workspace')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('existing-own-membership','existing-own-workspace','browser-existing','OWNER')`)
	router, err := NewRouter(db, "synthetic-invitation-browser-secret", newTestLogger(), WithAllowSignup(true))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	defer server.Close()
	fixture := map[string]string{"server": server.URL, "workspace": workspace, "password": password, "new_email": "new-reader@example.test", "existing_email": "existing@example.test", "new_token": "tok-browser-new", "existing_token": "tok-browser-existing"}
	raw, _ := json.Marshal(fixture)
	path := filepath.Join(t.TempDir(), "invitation-fixture.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", filepath.Join(repo, "scripts/acceptance-invitation-browser.mjs"))
	command.Dir = repo
	command.Env = append(os.Environ(), "CREWSHIP_INVITATION_FIXTURE="+path)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("real invitation browser acceptance", err)
	}
	var accepted, members int
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id IN ('browser-new','browser-existing') AND accepted_at IS NOT NULL`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND u.email IN ('new-reader@example.test','existing@example.test')`, workspace).Scan(&members); err != nil {
		t.Fatal(err)
	}
	var restricted int
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND access_mode='restricted'`, workspace).Scan(&restricted); err != nil {
		t.Fatal(err)
	}
	if accepted != 2 || members != 2 || restricted != 2 {
		t.Fatal("browser handoff DB effect", accepted, members)
	}
}
