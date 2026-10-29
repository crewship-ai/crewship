package api

import (
	"context"
	"encoding/json"
	"errors"
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

type persistedSessionFrameWriter struct {
	*httptest.ResponseRecorder
	afterFirst func()
	cancel     context.CancelFunc
	writes     int
}

func (w *persistedSessionFrameWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(b)
	w.writes++
	if w.writes == 1 {
		w.afterFirst()
	} else {
		// Bound the broken implementation after it actually releases a frame.
		w.cancel()
	}
	return n, err
}
func (w *persistedSessionFrameWriter) Flush() { w.ResponseRecorder.Flush() }

func TestPersistedSessionRevocationStopsHTTPFrames(t *testing.T) {
	for _, transport := range []string{"journal", "ndjson"} {
		t.Run(transport, func(t *testing.T) {
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			workspace := seedTestWorkspace(t, db, user)
			router, err := NewRouter(db, "test-secret-for-jwt-signing-32chars!!", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			session, err := router.sessionsStore.Create(t.Context(), user, "", "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			other, err := router.sessionsStore.Create(t.Context(), user, "", "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			otherCtx, release, err := router.authMw.authenticateStream(t.Context(), &auth.Claims{ID: user, Sid: other.ID})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			bearer, err := router.authMw.validator.IssueAccessToken(user, session.ID, "Test", "test@example.com")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("X-Workspace-ID", workspace)
			var handler http.Handler
			if transport == "journal" {
				h := NewJournalHandler(db, newTestLogger(), nil)
				seedJournalRow(t, h, "persisted-before", workspace, string(journal.EntryRunStarted), "info", "positive-control", time.Now().Add(-time.Minute))
				seedJournalRow(t, h, "persisted-after", workspace, string(journal.EntryRunCompleted), "info", "external-revoke-canary", time.Now())
				handler = router.authMw.RequireAuth(router.authMw.RequireWorkspace(http.HandlerFunc(h.Stream)))
			} else {
				source := &fakeRunStreamSource{Hub: newTestHubForStream(t), allow: true, replay: ws.SessionReplay{Found: true, Frames: [][]byte{[]byte(`{"type":"chat_event","channel":"session:queued","seq":1,"payload":{"type":"text","content":"external-revoke-canary"}}`)}}}
				req.SetPathValue("chatId", "queued")
				handler = router.authMw.RequireAuth(http.HandlerFunc(NewRunStreamHandler(source, newTestLogger()).Stream))
			}
			out := &persistedSessionFrameWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, afterFirst: func() {
				// Native persistent mutation: deliberately bypass every notifier.
				execOrFatal(t, db, `UPDATE user_sessions SET revoked_at=CURRENT_TIMESTAMP,revoked_reason='password_change' WHERE id=?`, session.ID)
			}}
			handler.ServeHTTP(out, req)
			if out.Code != http.StatusOK || (transport == "journal" && !strings.Contains(out.Body.String(), "positive-control")) || (transport == "ndjson" && !strings.Contains(out.Body.String(), "stream.open")) {
				t.Fatalf("opening calibration failed: HTTP%d %s", out.Code, out.Body.String())
			}
			if strings.Contains(out.Body.String(), "external-revoke-canary") || out.writes != 1 {
				t.Errorf("persistent session revoke released a later frame: writes=%d body=%s", out.writes, out.Body.String())
			}
			if !auth.StreamCredentialCurrent(otherCtx) {
				t.Error("unrelated same-user session was invalidated")
			}
		})
	}
}

func TestPersistedSessionRevocationStopsWebSocketControl(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	validator := newTestJWTValidator(t)
	hub := ws.NewHub(newTestLogger(), nil, validator, sessions.NewDBStore(db))
	router, err := NewRouter(db, "test-secret-for-jwt-signing-32chars!!", newTestLogger(), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	defer srv.Close()
	open := func() (*websocket.Conn, string) {
		t.Helper()
		s, err := router.sessionsStore.Create(t.Context(), user, "", "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		bearer, err := validator.IssueAccessToken(user, s.ID, "Test", "test@example.com")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ws-token", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		var ticket struct {
			Token string `json:"token"`
		}
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &ticket) != nil || ticket.Token == "" {
			t.Fatalf("ticket status%d", rr.Code)
		}
		conn, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), "", srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := websocket.JSON.Send(conn, map[string]string{"type": "auth", "token": ticket.Token}); err != nil {
			t.Fatal(err)
		}
		return conn, s.ID
	}
	ping := func(conn *websocket.Conn) error {
		if err := websocket.JSON.Send(conn, map[string]string{"type": "ping"}); err != nil {
			return err
		}
		var frame ws.ServerMessage
		if err := websocket.JSON.Receive(conn, &frame); err != nil {
			return err
		}
		if frame.Type != "pong" {
			t.Fatalf("expected pong, got %q", frame.Type)
		}
		return nil
	}
	conn, sid := open()
	other, _ := open()
	if err := ping(conn); err != nil {
		t.Fatal(err)
	}
	if err := ping(other); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE user_sessions SET revoked_at=CURRENT_TIMESTAMP,revoked_reason='password_change' WHERE id=?`, sid)
	if err := ping(other); err != nil {
		t.Fatalf("unrelated session lost control: %v", err)
	}
	if err := ping(conn); err == nil {
		t.Error("externally revoked session delivered a new pong")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("revoked socket timed out instead of closing before its next control frame")
	}
}

// Store errors are not cached as permission, nor converted into a global
// registry failure. All rows remain native DB sessions; only this lookup faults.
type persistedSessionFaultStore struct {
	sessions.Store
	failID string
}

func (s *persistedSessionFaultStore) Get(ctx context.Context, id string) (*sessions.Session, error) {
	if id == s.failID {
		return nil, errors.New("session lookup unavailable")
	}
	return s.Store.Get(ctx, id)
}

func TestPersistedSessionLookupFailureIsCredentialLocal(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	store := &persistedSessionFaultStore{Store: sessions.NewDBStore(db)}
	m := NewAuthMiddleware(newTestJWTValidator(t), store, db, newTestLogger())
	own, err := store.Create(t.Context(), user, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(t.Context(), user, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := m.authenticateStream(t.Context(), &auth.Claims{ID: user, Sid: own.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	otherCtx, releaseOther, err := m.authenticateStream(t.Context(), &auth.Claims{ID: user, Sid: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOther()
	store.failID = own.ID
	if auth.StreamCredentialCurrent(ctx) || ctx.Err() == nil {
		t.Error("failed session query did not fence and cancel its stream")
	}
	if !auth.StreamCredentialCurrent(otherCtx) {
		t.Error("session query failure invalidated an unrelated credential")
	}
}
