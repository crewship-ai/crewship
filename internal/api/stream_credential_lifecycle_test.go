package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
	"golang.org/x/net/websocket"
)

type credentialRevokeWriter struct {
	*httptest.ResponseRecorder
	revoke func()
}

func (w *credentialRevokeWriter) Write(b []byte) (int, error) {
	n, e := w.ResponseRecorder.Write(b)
	if w.revoke != nil {
		f := w.revoke
		w.revoke = nil
		f()
	}
	return n, e
}
func (w *credentialRevokeWriter) Flush() { w.ResponseRecorder.Flush() }

// Revoke synchronously after the first actual frame, before the next queued
// frame is authorized. This also exercises replay rather than only live polling.
func TestStreamCredentialRevocationStopsQueuedFrames(t *testing.T) {
	for _, kind := range []string{"session", "token"} {
		for _, transport := range []string{"journal", "ndjson"} {
			t.Run(kind+"/"+transport, func(t *testing.T) {
				db := setupTestDB(t)
				user := seedTestUser(t, db)
				workspace := seedTestWorkspace(t, db, user)
				router, err := NewRouter(db, "test-secret-for-jwt-signing-32chars!!", newTestLogger())
				if err != nil {
					t.Fatal(err)
				}
				var bearer string
				var revoke func()
				if kind == "session" {
					s, e := router.sessionsStore.Create(context.Background(), user, "", "", time.Hour)
					if e != nil {
						t.Fatal(e)
					}
					bearer, e = router.authMw.validator.IssueAccessToken(user, s.ID, "Test", "test@example.com")
					if e != nil {
						t.Fatal(e)
					}
					revoke = func() {
						if e := router.sessionsStore.Revoke(context.Background(), s.ID, sessions.ReasonLogout); e != nil {
							t.Fatal(e)
						}
					}
				} else {
					bearer = "crewship_cli_queued-frame-test"
					execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('queued-token',?,'test',?,'STANDARD')`, user, hashStandard(bearer))
					revoke = func() {
						req := httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/queued-token", nil)
						req.Header.Set("Authorization", "Bearer "+bearer)
						rr := httptest.NewRecorder()
						router.ServeHTTP(rr, req)
						if rr.Code != 200 {
							t.Fatalf("revoke status %d", rr.Code)
						}
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req := httptest.NewRequest("GET", "/stream", nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+bearer)
				req.Header.Set("X-Workspace-ID", workspace)
				var handler http.Handler
				if transport == "journal" {
					h := NewJournalHandler(db, newTestLogger(), nil)
					seedJournalRow(t, h, "credential-before", workspace, string(journal.EntryRunStarted), "info", "positive-control", time.Now().Add(-time.Minute))
					seedJournalRow(t, h, "credential-after", workspace, string(journal.EntryRunCompleted), "info", "after-revoke-canary", time.Now())
					handler = router.authMw.RequireAuth(router.authMw.RequireWorkspace(http.HandlerFunc(h.Stream)))
				} else {
					hub := newTestHubForStream(t)
					source := &fakeRunStreamSource{Hub: hub, allow: true, replay: ws.SessionReplay{Active: true, Found: true, FromSeq: 0, Frames: [][]byte{[]byte(`{"type":"chat_event","channel":"session:queued","seq":1,"payload":{"type":"text","content":"after-revoke-canary"}}`)}}}
					req.SetPathValue("chatId", "queued")
					handler = router.authMw.RequireAuth(http.HandlerFunc(NewRunStreamHandler(source, newTestLogger()).Stream))
				}
				out := &credentialRevokeWriter{ResponseRecorder: httptest.NewRecorder(), revoke: revoke}
				handler.ServeHTTP(out, req)
				if ctx.Err() != nil {
					t.Fatalf("parent canceled independently of revocation: %v", ctx.Err())
				}
				if out.Code != 200 {
					t.Fatalf("open status %d", out.Code)
				}
				if transport == "journal" && !strings.Contains(out.Body.String(), "positive-control") {
					t.Fatal("missing positive control")
				}
				if transport == "ndjson" && !strings.Contains(out.Body.String(), "stream.open") {
					t.Fatal("missing positive control")
				}
				if strings.Contains(out.Body.String(), "after-revoke-canary") {
					t.Fatal("credential-revoked stream delivered queued content")
				}
			})
		}
	}
}

// Exercise the real upgrade and ticket minting, rather than only the notifier.
// Revocation completes before the next control frame is requested.
func TestStreamCredentialWebSocketLifecycle(t *testing.T) {
	for _, kind := range []string{"session", "token"} {
		t.Run(kind, func(t *testing.T) {
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			validator := newTestJWTValidator(t)
			hub := ws.NewHub(newTestLogger(), nil, validator, sessions.NewDBStore(db))
			router, err := NewRouter(db, "test-secret-for-jwt-signing-32chars!!", newTestLogger(), WithHub(hub))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { hub.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			var bearer string
			var revoke func()
			if kind == "session" {
				s, e := router.sessionsStore.Create(ctx, user, "", "", time.Hour)
				if e != nil {
					t.Fatal(e)
				}
				bearer, e = validator.IssueAccessToken(user, s.ID, "Test", "test@example.com")
				if e != nil {
					t.Fatal(e)
				}
				revoke = func() {
					if e := router.sessionsStore.Revoke(context.Background(), s.ID, sessions.ReasonLogout); e != nil {
						t.Fatal(e)
					}
				}
			} else {
				bearer = "crewship_cli_ws-lifecycle-test"
				execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('ws-token',?,'test',?,'STANDARD')`, user, hashStandard(bearer))
				revoke = func() {
					req := httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/ws-token", nil)
					req.Header.Set("Authorization", "Bearer "+bearer)
					rr := httptest.NewRecorder()
					router.ServeHTTP(rr, req)
					if rr.Code != 200 {
						t.Fatalf("revoke %d", rr.Code)
					}
				}
			}
			req := httptest.NewRequest("GET", "/api/v1/ws-token", nil)
			req.Header.Set("Authorization", "Bearer "+bearer)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Fatalf("ticket %d", rr.Code)
			}
			var ticket struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &ticket); err != nil {
				t.Fatal(err)
			}
			claims, e := validator.ValidateWS(ticket.Token)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "token" && claims.CLITokenID != "ws-token" {
				t.Fatal("source token identity lost")
			}
			if kind == "session" && (claims.Sid == "" || claims.CredentialExpiresAt == 0) {
				t.Fatal("source session identity or expiry lost")
			}
			srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
			defer srv.Close()
			conn, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), "", srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if err := websocket.JSON.Send(conn, map[string]string{"type": "auth", "token": ticket.Token}); err != nil {
				t.Fatal(err)
			}
			if err := websocket.JSON.Send(conn, map[string]string{"type": "ping"}); err != nil {
				t.Fatal(err)
			}
			var frame ws.ServerMessage
			if err := websocket.JSON.Receive(conn, &frame); err != nil || frame.Type != "pong" {
				t.Fatalf("positive control %s %v", frame.Type, err)
			}
			revoke()
			// An already-authorized courtesy revocation frame may be queued. No pong
			// requested after revoke may be delivered, and an idle connection must close.
			_ = websocket.JSON.Send(conn, map[string]string{"type": "ping"})
			for {
				err := websocket.JSON.Receive(conn, &frame)
				if err != nil {
					if e, ok := err.(net.Error); ok && e.Timeout() {
						t.Fatal("revoked socket did not close")
					}
					break
				}
				if frame.Type == "pong" {
					t.Fatal("post-revocation frame delivered")
				}
			}
			// A ticket minted before revoke must not reconnect afterwards.
			late, e := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), "", srv.URL)
			if e != nil {
				t.Fatal(e)
			}
			defer late.Close()
			late.SetDeadline(time.Now().Add(3 * time.Second))
			if e := websocket.JSON.Send(late, map[string]string{"type": "auth", "token": ticket.Token}); e != nil {
				t.Fatal(e)
			}
			_ = websocket.JSON.Send(late, map[string]string{"type": "ping"})
			e = websocket.JSON.Receive(late, &frame)
			if e != nil {
				if timeout, ok := e.(net.Error); ok && timeout.Timeout() {
					t.Fatal("revoked ticket was neither rejected nor closed")
				}
			} else if frame.Type != "session_revoked" && frame.Type != "error" {
				t.Fatalf("revoked ticket admitted: %s", frame.Type)
			}
		})
	}
}

func TestStreamCredentialPasswordRevokesTokensKeepsCurrentSession(t *testing.T) {
	h, store, user := newProfileRig(t)
	router, err := NewRouter(h.db, "test-secret-for-jwt-signing-32chars!!", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Create(t.Context(), user, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokenCtx, release := router.authMw.streams.Track(t.Context(), router.authMw.streams.Epoch(), user, "", "password-token", time.Time{})
	defer release()
	browserCtx, closeBrowser := router.authMw.streams.Track(t.Context(), router.authMw.streams.Epoch(), user, session.ID, "", session.ExpiresAt)
	defer closeBrowser()
	execOrFatal(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('password-token',?,'test',?,'STANDARD')`, user, hashStandard("crewship_cli_password-test"))
	bearer, err := router.authMw.validator.IssueAccessToken(user, session.ID, "Test", "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/users/me/password", strings.NewReader(`{"current_password":"oldpassword1","new_password":"newpassword123","revoke_cli_tokens":true}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("password change %d", rr.Code)
	}
	if auth.StreamCredentialCurrent(tokenCtx) || !auth.StreamCredentialCurrent(browserCtx) {
		t.Fatal("token revoke must preserve current browser session")
	}
}

func TestStreamCredentialSuspensionNotifiesOnlyAfterCommit(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprint("rollback-", rollback), func(t *testing.T) {
			f := newInstanceFixture(t)
			execOrFatal(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('suspension-token','carol','test',?,'STANDARD')`, hashStandard("crewship_cli_suspension-test"))
			tracked, release := f.r.authMw.streams.Track(t.Context(), f.r.authMw.streams.Epoch(), "carol", "", "suspension-token", time.Time{})
			defer release()
			if rollback {
				execOrFatal(t, f.db, `CREATE TRIGGER reject_stream_token_revoke BEFORE UPDATE OF revoked_at ON cli_tokens BEGIN SELECT RAISE(ABORT,'synthetic rollback'); END`)
			}
			rr := f.do(f.boss, "POST", "/api/v1/admin/instance/people/carol/suspend", `{"reason":"test"}`)
			want := 200
			if rollback {
				want = 500
			}
			if rr.Code != want {
				t.Fatalf("suspend %d", rr.Code)
			}
			if auth.StreamCredentialCurrent(tracked) != rollback {
				t.Fatal("stream notification must match token transaction commit")
			}
		})
	}
}

func TestStreamCredentialPreservesAccessValidationGrace(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	validator := newTestJWTValidator(t)
	store := sessions.NewDBStore(db)
	session, err := store.Create(t.Context(), user, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	validator.SetClock(func() time.Time { return time.Now().Add(-auth.AccessTokenTTL - 2*time.Second) })
	bearer, err := validator.IssueAccessToken(user, session.ID, "Test", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mw := NewAuthMiddleware(validator, store, db, newTestLogger())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rr := httptest.NewRecorder()
	mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("existing five-second access grace lost: %d", rr.Code)
	}
}
func TestStreamCredentialMalformedTokenExpiryFailsClosed(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	bearer := "crewship_cli_malformed-expiry-test"
	execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,expires_at) VALUES('malformed-expiry',?,'test',?,'STANDARD','not-a-date')`, user, hashStandard(bearer))
	_, err := ValidateCLITokenFull(t.Context(), db, bearer, ValidateAuditContext{})
	if !errors.Is(err, errInvalidCLIToken) {
		t.Fatalf("malformed expiry accepted: %v", err)
	}
}

func TestStreamCredentialStoreErrorsPreserveClass(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	store := sessions.NewDBStore(db)
	session, err := store.Create(t.Context(), user, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mw := NewAuthMiddleware(newTestJWTValidator(t), store, db, newTestLogger())
	for _, claims := range []*auth.Claims{{ID: user, Sid: session.ID}, {ID: user, CLITokenID: "own-test-token"}} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tracked, release, err := mw.authenticateStream(ctx, claims)
		if release != nil {
			release()
		}
		if tracked != nil || !errors.Is(err, context.Canceled) {
			t.Errorf("storage error lost its class: tracked=%v err=%v", tracked != nil, err)
		}
	}
}
