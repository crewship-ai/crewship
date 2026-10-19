package terminal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/gorilla/websocket"
)

type standingFixture struct {
	db      *sql.DB
	handler *Handler
	router  http.Handler
	owner   string
	member  string
	token   string
	queries atomic.Int64
}

func newStandingFixture(t *testing.T, kind string, interval time.Duration) *standingFixture {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	db := seedTerminalDB(t)
	mustExec(t, db, `INSERT INTO users(id,email) VALUES('standing-owner','standing-owner@example.test')`)
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('standing-owner-member','w1','standing-owner','OWNER')`)
	mustExec(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE id='wm1'`)
	v := newTestValidator(t)
	f := &standingFixture{db: db}
	browser := func(id, email string) string {
		s, e := sessions.NewDBStore(db).Create(t.Context(), id, "", "", time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		token, e := v.IssueAccessToken(id, s.ID, "", email)
		if e != nil {
			t.Fatal(e)
		}
		return token
	}
	f.owner = browser("standing-owner", "standing-owner@example.test")
	f.member = browser("u1", "u1@x")
	var seq int
	var name, path string
	if e := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); e != nil || path == "" {
		t.Fatal("SETUP native database path", e)
	}
	counted := sql.OpenDB(&standingConnector{base: db.Driver(), dsn: path, queries: &f.queries})
	counted.SetMaxOpenConns(1)
	if _, e := counted.Exec(`PRAGMA foreign_keys=ON`); e != nil {
		t.Fatal(e)
	}
	var fk int
	if e := counted.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); e != nil || fk != 1 {
		t.Fatal("SETUP counted native connection FK", e)
	}
	t.Cleanup(func() { _ = counted.Close() })
	// A fresh physical connection uses the same native migrated file. It only
	// counts QueryContext calls, without replacing SQL results or driver behavior.
	f.handler = New(&covContainer{}, v, counted, silentLogger(), sessions.NewDBStore(db))
	f.handler.revokePollInterval = interval
	var e error
	f.router, e = api.NewRouter(db, "supersecretkeythatisatleast32chars!!", silentLogger(), api.WithSessionRevocationNotifiers(f.handler), api.WithInternalToken("standing-synthetic-internal"))
	if e != nil {
		t.Fatal(e)
	}
	f.token = f.member
	if kind == "cli" {
		rr := f.request("POST", "/api/v1/auth/cli-token", f.member, `{"name":"standing-controls","expires_in_seconds":300}`)
		if rr.Code != 200 {
			t.Fatalf("SETUP token status %d", rr.Code)
		}
		var issued struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(rr.Body.Bytes(), &issued) != nil || issued.Token == "" {
			t.Fatal("SETUP token shape")
		}
		f.token = issued.Token
	}
	return f
}

func (f *standingFixture) request(method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Workspace-ID", "w1")
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, r)
	return rr
}

func (f *standingFixture) open(t *testing.T, token string) (*websocket.Conn, net.Conn, <-chan struct{}) {
	t.Helper()
	rr := f.request("GET", "/api/v1/ws-token", token, "")
	if rr.Code != 200 {
		t.Fatalf("SETUP ticket status %d", rr.Code)
	}
	var ticket struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(rr.Body.Bytes(), &ticket) != nil || ticket.Token == "" {
		t.Fatal("SETUP ticket shape")
	}
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	f.handler.container = &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: server}
	c, done := dialTerminalDone(t, f.handler)
	if e := c.WriteJSON(map[string]string{"type": "auth", "token": ticket.Token}); e != nil {
		t.Fatal(e)
	}
	if e := c.WriteJSON(map[string]string{"crew_id": "c1", "crew_slug": "crew-a"}); e != nil {
		t.Fatal(e)
	}
	standingOutput(t, c, peer, "standing-positive")
	return c, peer, done
}

func standingOutput(t *testing.T, c *websocket.Conn, peer net.Conn, want string) {
	t.Helper()
	w := make(chan error, 1)
	go func() {
		_ = peer.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, e := peer.Write([]byte(want))
		w <- e
	}()
	if got := string(readBinaryFrame(t, c)); got != want {
		t.Fatal("unexpected PTY canary")
	}
	if e := <-w; e != nil {
		t.Fatal(e)
	}
}

func TestTerminalStandingAuthorityControls(t *testing.T) {
	for _, kind := range []string{"cli", "browser"} {
		for _, direction := range []string{"input", "output", "idle", "resize", "pong"} {
			t.Run(kind+"/"+direction, func(t *testing.T) {
				interval := time.Hour
				if direction == "idle" {
					interval = 20 * time.Millisecond
				}
				f := newStandingFixture(t, kind, interval)
				if kind == "cli" && direction == "idle" {
					f.handler.sessionStore = nil
				}
				c, peer, done := f.open(t, f.token)
				defer func() {
					_ = c.Close()
					_ = peer.Close()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("handler cleanup stalled")
					}
				}()
				rr := f.request("PATCH", "/api/v1/workspaces/w1/members/wm1", f.owner, `{"role":"VIEWER"}`)
				if rr.Code != 200 {
					t.Fatalf("SETUP role mutation status %d", rr.Code)
				}
				switch direction {
				case "input":
					_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
					if e := c.WriteMessage(websocket.BinaryMessage, []byte("DENIED_INPUT")); e != nil {
						t.Fatal("SETUP input enqueue", e)
					}
					b := make([]byte, 64)
					n, e := peer.Read(b)
					if n > 0 {
						t.Error("PTY accepted input after standing authority loss")
					} else if ne, ok := e.(net.Error); ok && ne.Timeout() {
						t.Error("PTY input remained open after standing authority loss")
					}
				case "output":
					w := make(chan struct{})
					go func() {
						defer close(w)
						_ = peer.SetWriteDeadline(time.Now().Add(time.Second))
						_, _ = peer.Write([]byte("DENIED_OUTPUT"))
					}()
					_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
					kind, data, e := c.ReadMessage()
					if e == nil {
						t.Errorf("output delivered after loss: frame_kind=%d own_canary=%t", kind, string(data) == "DENIED_OUTPUT")
					} else if ne, ok := e.(net.Error); ok && ne.Timeout() {
						t.Error("output remained open after loss")
					}
					<-w
				case "resize":
					if e := c.WriteJSON(map[string]any{"type": "resize", "rows": 50, "cols": 100}); e != nil {
						t.Fatal(e)
					}
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("resize did not close unauthorized terminal")
					}
					if got := f.handler.container.(*covInteractive).resizes(); len(got) != 0 {
						t.Error("unauthorized resize reached PTY")
					}
				case "pong":
					var pongs atomic.Int64
					c.SetPongHandler(func(string) error { pongs.Add(1); return nil })
					if e := c.WriteControl(websocket.PingMessage, []byte("standing-ping"), time.Now().Add(time.Second)); e != nil {
						t.Fatal(e)
					}
					_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
					_, _, e := c.ReadMessage()
					if ne, ok := e.(net.Error); ok && ne.Timeout() {
						t.Error("unauthorized ping did not close terminal")
					}
					if pongs.Load() != 0 {
						t.Error("unauthorized ping received pong")
					}
				case "idle":
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("idle terminal survived standing authority loss")
					}
				}
			})
		}
	}
}

func TestTerminalStandingAuthorityOtherUserAndQueryCost(t *testing.T) {
	f := newStandingFixture(t, "cli", time.Hour)
	c, peer, done := f.open(t, f.owner)
	defer func() {
		_ = c.Close()
		_ = peer.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("cleanup stalled")
		}
	}()
	rr := f.request("PATCH", "/api/v1/workspaces/w1/members/wm1", f.owner, `{"role":"VIEWER"}`)
	if rr.Code != 200 {
		t.Fatalf("SETUP other-user mutation %d", rr.Code)
	}
	before := f.queries.Load()
	for i := 0; i < 5; i++ {
		if e := c.WriteMessage(websocket.BinaryMessage, []byte("healthy-input")); e != nil {
			t.Fatal(e)
		}
		_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 64)
		n, e := peer.Read(b)
		if e != nil || string(b[:n]) != "healthy-input" {
			t.Fatal("unaffected user input failed")
		}
		standingOutput(t, c, peer, "healthy-output")
	}
	if got := f.queries.Load() - before; got != 10 {
		t.Errorf("five input/output pairs: got %d native queries, want exactly 10", got)
	}
	t.Logf("native resource SQL queries per 10 data frames: %d", f.queries.Load()-before)
}

func TestTerminalStandingAuthorityDatabaseFailure(t *testing.T) {
	f := newStandingFixture(t, "cli", 20*time.Millisecond)
	c, peer, done := f.open(t, f.token)
	defer c.Close()
	defer peer.Close()
	if e := f.handler.db.Close(); e != nil {
		t.Fatal(e)
	}
	if e := f.handler.verifyAccess(t.Context(), "u1", "c1"); e == nil || errors.Is(e, sql.ErrNoRows) {
		t.Fatal("operational DB failure was confused with absent membership")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("idle terminal survived operational resource-check failure")
	}
}

func TestTerminalStandingAuthorityLiveResourceQuery(t *testing.T) {
	for _, field := range []string{"user", "workspace", "crew"} {
		t.Run(field, func(t *testing.T) {
			f := newStandingFixture(t, "cli", time.Hour)
			if e := f.handler.verifyAccess(t.Context(), "u1", "c1"); e != nil {
				t.Fatal("SETUP healthy resource", e)
			}
			// Native SQL fixtures isolate each live-field predicate, not an HTTP writer proof.
			q := map[string]string{"user": `UPDATE users SET suspended_at='2026-10-19T00:00:00Z' WHERE id='u1'`, "workspace": `UPDATE workspaces SET deleted_at='2026-10-19T00:00:00Z' WHERE id='w1'`, "crew": `UPDATE crews SET deleted_at='2026-10-19T00:00:00Z' WHERE id='c1'`}[field]
			mustExec(t, f.db, q)
			if e := f.handler.verifyAccess(t.Context(), "u1", "c1"); e == nil {
				t.Error("non-live resource passed standing query")
			}
		})
	}
}

// Thin native driver decoration for exact read-query accounting.
type standingConnector struct {
	base    driver.Driver
	dsn     string
	queries *atomic.Int64
}

func (c *standingConnector) Connect(context.Context) (driver.Conn, error) {
	v, e := c.base.Open(c.dsn)
	if e != nil {
		return nil, e
	}
	return &standingConn{Conn: v, queries: c.queries}, nil
}
func (c *standingConnector) Driver() driver.Driver { return c.base }

type standingConn struct {
	driver.Conn
	queries *atomic.Int64
}

func (c *standingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	if v, ok := c.Conn.(driver.QueryerContext); ok {
		return v.QueryContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

// A native role mutation during container preparation must be observed before
// ExecInteractive, rather than trusting the earlier admission snapshot.
func TestTerminalStandingAuthorityAfterContainerPreparation(t *testing.T) {
	f := newStandingFixture(t, "cli", time.Hour)
	rr := f.request("GET", "/api/v1/ws-token", f.token, "")
	if rr.Code != 200 {
		t.Fatalf("SETUP ticket %d", rr.Code)
	}
	var ticket struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(rr.Body.Bytes(), &ticket) != nil || ticket.Token == "" {
		t.Fatal("SETUP ticket shape")
	}
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: server}
	var changed atomic.Bool
	f.handler.container = &standingPreparation{covInteractive: im, before: func() {
		result := f.request("PATCH", "/api/v1/workspaces/w1/members/wm1", f.owner, `{"role":"VIEWER"}`)
		if result.Code != 200 {
			t.Errorf("SETUP preparation mutation %d", result.Code)
		} else {
			changed.Store(true)
		}
	}}
	c, done := dialTerminalDone(t, f.handler)
	defer c.Close()
	if e := c.WriteJSON(map[string]string{"type": "auth", "token": ticket.Token}); e != nil {
		t.Fatal(e)
	}
	if e := c.WriteJSON(map[string]string{"crew_id": "c1", "crew_slug": "crew-a"}); e != nil {
		t.Fatal(e)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, e := c.ReadMessage()
	if ne, ok := e.(net.Error); ok && ne.Timeout() {
		t.Error("preparation authority loss did not close terminal")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("preparation cleanup stalled")
	}
	if !changed.Load() {
		t.Fatal("SETUP native preparation mutation not reached")
	}
	if im.config() != nil {
		t.Error("PTY started after authority loss during container preparation")
	}
}

type standingPreparation struct {
	*covInteractive
	before func()
}

func (p *standingPreparation) EnsureCrewRuntime(ctx context.Context, c provider.CrewConfig) (string, error) {
	p.before()
	return p.covContainer.EnsureCrewRuntime(ctx, c)
}
