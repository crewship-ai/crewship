package terminal

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// These are standing resource-authority counterexamples, separate from the
// credential revoke/expiry contract. All final mutations use the real Router.
func TestTerminalWorkspaceAuthorityAfterMutation(t *testing.T) {
	for _, change := range []string{"demote-viewer", "remove-member", "restrict-member"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
			db := seedTerminalDB(t)
			// Initial native fixture: an independent OWNER and a trusted MEMBER target.
			mustExec(t, db, `INSERT INTO users(id,email) VALUES('terminal-owner','owner@example.test')`)
			mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('terminal-owner-member','w1','terminal-owner','OWNER')`)
			mustExec(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE id='wm1'`)
			v := newTestValidator(t)
			serverSide, peer := net.Pipe()
			t.Cleanup(func() { _ = serverSide.Close(); _ = peer.Close() })
			im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
			store := sessions.NewDBStore(db)
			h := New(im, v, db, silentLogger(), store)
			router, err := api.NewRouter(db, "supersecretkeythatisatleast32chars!!", silentLogger(), api.WithSessionRevocationNotifiers(h), api.WithInternalToken("synthetic-terminal-authority"))
			if err != nil {
				t.Fatal("SETUP router", err)
			}
			browser := func(id, email string) string {
				s, e := store.Create(t.Context(), id, "", "", time.Hour)
				if e != nil {
					t.Fatal("SETUP session", e)
				}
				token, e := v.IssueAccessToken(id, s.ID, "", email)
				if e != nil {
					t.Fatal("SETUP access token", e)
				}
				return token
			}
			owner := browser("terminal-owner", "owner@example.test")
			member := browser("u1", "u1@x")
			request := func(method, path, bearer, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+bearer)
				r.Header.Set("X-Workspace-ID", "w1")
				r.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, r)
				return rr
			}
			rr := request("POST", "/api/v1/auth/cli-token", member, `{"name":"terminal-authority-test","tier":"STANDARD","expires_in_seconds":300,"scopes":[]}`)
			if rr.Code != 200 {
				t.Fatalf("SETUP issue token status %d", rr.Code)
			}
			var issued struct {
				ID    string `json:"id"`
				Token string `json:"token"`
			}
			if json.Unmarshal(rr.Body.Bytes(), &issued) != nil || issued.ID == "" || issued.Token == "" {
				t.Fatal("SETUP issued token shape")
			}
			mint := func() (string, int) {
				rr := request("GET", "/api/v1/ws-token", issued.Token, "")
				if rr.Code != 200 {
					return "", rr.Code
				}
				var ticket struct {
					Token string `json:"token"`
				}
				if json.Unmarshal(rr.Body.Bytes(), &ticket) != nil || ticket.Token == "" {
					t.Fatal("SETUP ticket shape")
				}
				return ticket.Token, rr.Code
			}
			ticket, status := mint()
			if status != 200 {
				t.Fatalf("SETUP initial ticket status %d", status)
			}
			conn, done := dialTerminalDone(t, h)
			if err := conn.WriteJSON(map[string]string{"type": "auth", "token": ticket}); err != nil {
				t.Fatal("SETUP auth write", err)
			}
			if err := conn.WriteJSON(map[string]string{"crew_id": "c1", "crew_slug": "crew-a"}); err != nil {
				t.Fatal("SETUP init write", err)
			}
			positive := make(chan error, 1)
			go func() {
				_ = peer.SetWriteDeadline(time.Now().Add(3 * time.Second))
				_, e := peer.Write([]byte("resource-positive"))
				positive <- e
			}()
			if string(readBinaryFrame(t, conn)) != "resource-positive" {
				t.Fatal("SETUP positive PTY canary")
			}
			if err := <-positive; err != nil {
				t.Fatal("SETUP positive write", err)
			}

			path := "/api/v1/workspaces/w1/members/wm1"
			switch change {
			case "demote-viewer":
				rr = request("PATCH", path, owner, `{"role":"VIEWER"}`)
			case "remove-member":
				rr = request("DELETE", path, owner, "")
			case "restrict-member":
				rr = request("GET", path+"/access", owner, "")
				if rr.Code != 200 {
					t.Fatalf("SETUP current policy status %d", rr.Code)
				}
				var policy access.Policy
				if json.Unmarshal(rr.Body.Bytes(), &policy) != nil || policy.ID != "wm1" || policy.Revision < 1 {
					t.Fatal("SETUP current policy shape")
				}
				policy.Mode = "restricted"
				policy.Rights = []access.Right{}
				body, e := json.Marshal(policy)
				if e != nil {
					t.Fatal(e)
				}
				rr = request("PUT", path+"/access", owner, string(body))
			}
			if rr.Code != 200 {
				t.Fatalf("SETUP native mutation status %d", rr.Code)
			}
			// A successful HTTP response alone is not proof of the target state.
			var expectedState int
			stateSQL := map[string]string{
				"demote-viewer":   `SELECT COUNT(*) FROM workspace_members WHERE id='wm1' AND user_id='u1' AND workspace_id='w1' AND role='VIEWER' AND access_mode='trusted'`,
				"remove-member":   `SELECT CASE WHEN COUNT(*)=0 THEN 1 ELSE 0 END FROM workspace_members WHERE user_id='u1' AND workspace_id='w1'`,
				"restrict-member": `SELECT COUNT(*) FROM workspace_members WHERE id='wm1' AND user_id='u1' AND workspace_id='w1' AND role='MEMBER' AND access_mode='restricted'`,
			}[change]
			if err := db.QueryRow(stateSQL).Scan(&expectedState); err != nil || expectedState != 1 {
				t.Fatal("SETUP native mutation final state mismatch")
			}
			var active int
			if err := db.QueryRow(`SELECT COUNT(*) FROM cli_tokens ct JOIN users u ON u.id=ct.user_id WHERE ct.id=? AND ct.revoked_at IS NULL AND u.suspended_at IS NULL`, issued.ID).Scan(&active); err != nil || active != 1 {
				t.Fatal("SETUP source credential unexpectedly revoked")
			}
			if err := h.verifyAccess(t.Context(), "u1", "c1"); err == nil {
				t.Fatal("SETUP fresh terminal resource access not denied")
			}
			// Fresh native mint is either globally restricted (403/404), or returns a
			// legitimate ticket whose *new* PTY admission must fail before execution.
			fresh, code := mint()
			if code == 200 {
				denied, deniedDone := dialTerminalDone(t, h)
				if err := denied.WriteJSON(map[string]string{"type": "auth", "token": fresh}); err != nil {
					t.Fatal(err)
				}
				if err := denied.WriteJSON(map[string]string{"crew_id": "c1", "crew_slug": "crew-a"}); err != nil {
					t.Fatal(err)
				}
				_ = denied.SetReadDeadline(time.Now().Add(2 * time.Second))
				var msg struct {
					Type string `json:"type"`
				}
				if err := denied.ReadJSON(&msg); err != nil || msg.Type != "error" {
					t.Fatal("SETUP fresh PTY denial missing")
				}
				_ = denied.Close()
				select {
				case <-deniedDone:
				case <-time.After(3 * time.Second):
					t.Fatal("SETUP fresh denial cleanup stalled")
				}
			} else if change != "restrict-member" || (code != 403 && code != 404) {
				t.Fatalf("SETUP fresh mint unexpected status %d", code)
			}

			wrote := make(chan struct{})
			go func() {
				defer close(wrote)
				_ = peer.SetWriteDeadline(time.Now().Add(time.Second))
				_, _ = peer.Write([]byte("AFTER_RESOURCE_AUTHORITY_LOSS"))
			}()
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			kind, data, readErr := conn.ReadMessage()
			if readErr == nil {
				if kind != 2 || string(data) != "AFTER_RESOURCE_AUTHORITY_LOSS" {
					t.Errorf("SETUP unexpected post-mutation frame kind=%d own_canary=%t", kind, string(data) == "AFTER_RESOURCE_AUTHORITY_LOSS")
				} else {
					t.Errorf("terminal delivered post-mutation output after resource authority loss: frame_kind=%d own_canary=true", kind)
				}
			} else if e, ok := readErr.(net.Error); ok && e.Timeout() {
				t.Error("terminal remained open after resource authority loss")
			}
			_ = conn.Close()
			_ = peer.Close()
			<-wrote
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("terminal cleanup stalled")
			}
		})
	}
}
