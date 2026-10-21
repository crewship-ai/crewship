package terminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestTerminalStreamCredentialLifetime(t *testing.T) {
	for _, change := range []string{"revoke-token", "suspend", "expire", "session"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
			db := seedTerminalDB(t)
			v := newTestValidator(t)
			serverSide, peer := net.Pipe()
			t.Cleanup(func() { _ = peer.Close() })
			im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
			h := New(im, v, db, silentLogger(), sessions.NewDBStore(db))
			router, err := api.NewRouter(db, "supersecretkeythatisatleast32chars!!", silentLogger(), api.WithSessionRevocationNotifiers(h), api.WithInternalToken("synthetic-terminal-internal"))
			if err != nil {
				t.Fatal(err)
			}
			const bearer = "crewship_cli_terminal-lifetime-synthetic"
			digest := sha256.Sum256([]byte(bearer))
			mustExec(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('terminal-token','u1','test',?,'STANDARD')`, hex.EncodeToString(digest[:]))
			req := httptest.NewRequest("GET", "/api/v1/ws-token", nil)
			req.Header.Set("Authorization", "Bearer "+bearer)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Fatalf("SETUP ticket status %d", rr.Code)
			}
			var ticket struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &ticket); err != nil {
				t.Fatal(err)
			}
			if change == "session" {
				s, e := sessions.NewDBStore(db).Create(t.Context(), "u1", "", "", time.Hour)
				if e != nil {
					t.Fatal(e)
				}
				bearerSession, e := v.IssueAccessToken("u1", s.ID, "", "u1@x")
				if e != nil {
					t.Fatal(e)
				}
				req = httptest.NewRequest("GET", "/api/v1/ws-token", nil)
				req.Header.Set("Authorization", "Bearer "+bearerSession)
				rr = httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != 200 {
					t.Fatalf("SETUP browser ticket %d", rr.Code)
				}
				if e = json.Unmarshal(rr.Body.Bytes(), &ticket); e != nil {
					t.Fatal(e)
				}
				defer func() {
					req := httptest.NewRequest("POST", "/api/v1/auth/sessions/"+s.ID+"/revoke", nil)
					req.Header.Set("Authorization", "Bearer "+bearerSession)
					rr := httptest.NewRecorder()
					router.ServeHTTP(rr, req)
					if rr.Code != 200 {
						t.Errorf("browser session cleanup status %d", rr.Code)
					}
				}()
			}
			var expiry time.Time
			if change == "expire" {
				expiry = time.Now().Add(3 * time.Second).Truncate(time.Second)
				mustExec(t, db, `UPDATE cli_tokens SET expires_at=? WHERE id='terminal-token'`, expiry.Format(time.RFC3339))
				// Remint through the real endpoint after the persisted expiry is set.
				req = httptest.NewRequest("GET", "/api/v1/ws-token", nil)
				req.Header.Set("Authorization", "Bearer "+bearer)
				rr = httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != 200 {
					t.Fatalf("SETUP expiring source ticket %d", rr.Code)
				}
				if err = json.Unmarshal(rr.Body.Bytes(), &ticket); err != nil {
					t.Fatal(err)
				}
			}
			conn, done := dialTerminalDone(t, h)
			if err := conn.WriteJSON(map[string]string{"type": "auth", "token": ticket.Token}); err != nil {
				t.Fatal(err)
			}
			if err := conn.WriteJSON(map[string]string{"crew_id": "c1", "crew_slug": "crew-a"}); err != nil {
				t.Fatal(err)
			}
			ready := make(chan error, 1)
			go func() {
				_ = peer.SetWriteDeadline(time.Now().Add(3 * time.Second))
				_, e := peer.Write([]byte("credential-positive"))
				ready <- e
			}()
			if got := string(readBinaryFrame(t, conn)); got != "credential-positive" {
				t.Fatalf("SETUP shell positive %q", got)
			}
			if err := <-ready; err != nil {
				t.Fatal(err)
			}
			var ping func(string) error
			h.sessions.Range(func(_, value any) bool { ping = value.(*Session).ws.PingHandler(); return false })
			if ping == nil {
				t.Fatal("SETUP active terminal registration missing")
			}
			switch change {
			case "revoke-token":
				req = httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/terminal-token", nil)
				req.Header.Set("Authorization", "Bearer "+bearer)
				rr = httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != 200 {
					t.Fatalf("SETUP revoke status %d", rr.Code)
				}
			case "suspend":
				mustExec(t, db, `INSERT INTO users(id,email,instance_role) VALUES('terminal-admin','terminal-admin@example.test','ADMIN')`)
				s, err := sessions.NewDBStore(db).Create(context.Background(), "terminal-admin", "", "", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				admin, err := v.IssueAccessToken("terminal-admin", s.ID, "Admin", "terminal-admin@example.test")
				if err != nil {
					t.Fatal(err)
				}
				req = httptest.NewRequest("POST", "/api/v1/admin/instance/people/u1/suspend", strings.NewReader(`{}`))
				req.Header.Set("Authorization", "Bearer "+admin)
				rr = httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != 200 && rr.Code != 204 {
					t.Fatalf("SETUP suspend status %d", rr.Code)
				}
			case "expire":
				<-time.After(time.Until(expiry))
			}
			if change == "session" {
				// A normal browser session still has output and controls.
				if e := ping("healthy"); e != nil {
					t.Fatalf("healthy ping %v", e)
				}
				_ = conn.Close()
				_ = peer.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("browser cleanup stalled")
				}
				return
			}
			if e := ping("after-loss"); !errors.Is(e, context.Canceled) {
				t.Errorf("control frame not fenced after %s: %v", change, e)
			}
			wrote := make(chan struct{})
			go func() {
				defer close(wrote)
				_ = peer.SetWriteDeadline(time.Now().Add(time.Second))
				_, _ = peer.Write([]byte("AFTER_CREDENTIAL_LOSS"))
			}()
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, data, readErr := conn.ReadMessage()
			if readErr == nil {
				t.Errorf("terminal delivered a frame after %s: %q", change, data)
			} else if e, ok := readErr.(net.Error); ok && e.Timeout() {
				t.Errorf("terminal remained open after %s", change)
			}
			_ = conn.Close()
			_ = peer.Close()
			<-wrote
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("terminal handler did not clean up")
			}
			if im.config() == nil {
				t.Fatal("SETUP did not open actual PTY")
			}
		})
	}
}

func TestTerminalStreamCredentialAdmission(t *testing.T) {
	for _, kind := range []string{"legacy", "unknown-token", "foreign-owner", "revoked", "expired", "restricted", "closed-store"} {
		t.Run(kind, func(t *testing.T) {
			db := seedTerminalDB(t)
			v := newTestValidator(t)
			a, b := net.Pipe()
			t.Cleanup(func() { _ = b.Close() })
			im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: a}
			h := New(im, v, db, silentLogger(), sessions.NewDBStore(db))
			_, err := api.NewRouter(db, "supersecretkeythatisatleast32chars!!", silentLogger(), api.WithSessionRevocationNotifiers(h), api.WithInternalToken("synthetic-terminal-internal"))
			if err != nil {
				t.Fatal(err)
			}
			owner := "u1"
			if kind == "foreign-owner" {
				owner = "other"
				mustExec(t, db, `INSERT INTO users(id,email) VALUES('other','other@example.test')`)
			}
			mustExec(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('admission-token',?,'test','synthetic-unused-hash','STANDARD')`, owner)
			ticket, err := v.IssueCredentialWSTicket("u1", "", "admission-token", "", "u1@x", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "legacy":
				ticket, err = v.IssueWSTicket("u1", "", "", "u1@x")
			case "unknown-token":
				ticket, err = v.IssueCredentialWSTicket("u1", "", "absent-token", "", "u1@x", time.Time{})
			case "revoked":
				mustExec(t, db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE id='admission-token'`)
			case "expired":
				mustExec(t, db, `UPDATE cli_tokens SET expires_at='2026-01-01T00:00:00Z' WHERE id='admission-token'`)
			case "restricted":
				mustExec(t, db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id='u1'`)
			case "closed-store":
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			conn, done := dialTerminalDone(t, h)
			if err := conn.WriteJSON(map[string]string{"type": "auth", "token": ticket}); err != nil {
				t.Fatal(err)
			}
			if err := conn.WriteJSON(map[string]string{"crew_id": "c1", "crew_slug": "crew-a"}); err != nil {
				t.Fatal(err)
			}
			got := readJSONFrame(t, conn)
			if got["type"] != "error" {
				t.Fatalf("invalid credential admitted: %+v", got)
			}
			if kind == "closed-store" && got["message"] != "credential unavailable" {
				t.Fatalf("operational error misclassified: %+v", got)
			}
			if im.config() != nil {
				t.Fatal("invalid credential opened PTY")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("rejected terminal did not return")
			}
		})
	}
}

// A real lease can lose authority between the authenticator's final check and
// its caller resuming. Even the refusal frame must use that tracked context.
func TestTerminalStreamCredentialAdmissionRaceWritesNothing(t *testing.T) {
	v := newTestValidator(t)
	h := New(&mockContainer{state: "running"}, v, seedTerminalDB(t), silentLogger(), nil)
	registry := &auth.StreamCredentials{}
	released, validBeforeRevoke := false, false
	h.SetStreamAuthenticator(func(ctx context.Context, c *auth.Claims) (context.Context, func(), error) {
		tracked, release := registry.Track(ctx, registry.Epoch(), c.ID, c.Sid, c.CLITokenID, time.Time{})
		validBeforeRevoke = auth.StreamCredentialCurrent(tracked)
		registry.NotifyTokenRevoked(c.CLITokenID)
		return tracked, func() { released = true; release() }, nil
	})
	ticket, err := v.IssueCredentialWSTicket("u1", "", "race-source", "", "u1@x", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	conn, done := dialTerminalDone(t, h)
	if err := conn.WriteJSON(map[string]string{"type": "auth", "token": ticket}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, data, e := conn.ReadMessage(); e == nil {
		t.Errorf("post-validation revoke emitted refusal frame: %q", data)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("invalidated admission did not return")
	}
	if !validBeforeRevoke {
		t.Fatal("SETUP valid registry admission was not established")
	}
	if !released {
		t.Fatal("invalidated admission did not release its lease")
	}
}
