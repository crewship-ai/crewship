//go:build linux && (docs_d0 || conformance)

package api

// D0 is deliberately a test-only slice. No Router registration, shipped
// migration, Page type or deployment depends on this experiment.
import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type d0Block struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}
type d0State struct {
	Page     string          `json:"page"`
	Rev      int             `json:"rev"`
	Epoch    int             `json:"epoch"`
	Schema   int             `json:"schema"`
	Snapshot []byte          `json:"snapshot"`
	Blocks   []d0Block       `json:"blocks"`
	Threads  json.RawMessage `json:"threads"`
}
type d0Commit struct {
	ID          string            `json:"id"`
	Fingerprint string            `json:"fingerprint"`
	Kind        string            `json:"kind"`
	Epoch       int               `json:"epoch"`
	Schema      int               `json:"schema"`
	IfRev       int               `json:"ifRev"`
	IfHashes    map[string]string `json:"ifHashes"`
	Snapshot    []byte            `json:"snapshot"`
	Blocks      []d0Block         `json:"blocks"`
	Thread      json.RawMessage   `json:"thread"`
	Checkpoint  int               `json:"checkpoint"`
}

func d0Read(ctx context.Context, tx *sql.Tx, page string) (d0State, error) {
	s := d0State{Page: page, Threads: json.RawMessage(`[]`)}
	var blocks []byte
	err := tx.QueryRowContext(ctx, `SELECT rev,epoch,schema_version,snapshot,blocks FROM d0_documents WHERE page_id=?`, page).Scan(&s.Rev, &s.Epoch, &s.Schema, &s.Snapshot, &blocks)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(blocks, &s.Blocks); err != nil {
		return s, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT body FROM d0_threads WHERE page_id=? ORDER BY id`, page)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	threads := []json.RawMessage{}
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return s, err
		}
		threads = append(threads, json.RawMessage(body))
	}
	if err = rows.Err(); err != nil {
		return s, err
	}
	s.Threads, err = json.Marshal(threads)
	return s, err
}

func TestDocsD0(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCS_D0") != "1" {
		t.Fatal("explicit D0 execution requires CREWSHIP_DOCS_D0=1 and installed prototype dependencies/Chromium")
	}
	h, workspace, owner, page, agentToken := artifactRunFixture(t)
	// General fixture copies use NORMAL for test throughput. This durability
	// experiment deliberately measures FULL on its single owned connection.
	h.db.SetMaxOpenConns(1)
	h.db.SetMaxIdleConns(1)
	execOrFatal(t, h.db, `PRAGMA synchronous=FULL`)
	var synchronous int
	if err := h.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("D0 requires synchronous FULL: %d %v", synchronous, err)
	}
	const editor = "d0-editor"
	execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'D0 second human')`, editor, editor+"@example.test")
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('d0-member',?,?,'VIEWER')`, workspace, editor)
	grant := func() {
		execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, editor, owner)
	}
	grant()
	// Only this migrated, temporary fixture database receives prototype tables.
	for _, statement := range []string{
		`CREATE TABLE d0_documents(page_id TEXT PRIMARY KEY REFERENCES pages(id),rev INTEGER NOT NULL,epoch INTEGER NOT NULL,schema_version INTEGER NOT NULL,snapshot BLOB NOT NULL,blocks BLOB NOT NULL)`,
		`CREATE TABLE d0_commits(page_id TEXT NOT NULL,rev INTEGER NOT NULL,actor TEXT NOT NULL,kind TEXT NOT NULL,request_id TEXT NOT NULL,fingerprint TEXT NOT NULL,snapshot BLOB NOT NULL,blocks BLOB NOT NULL,PRIMARY KEY(page_id,rev),UNIQUE(page_id,actor,request_id))`,
		`CREATE TABLE d0_threads(page_id TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(page_id,id))`,
		`CREATE TABLE d0_checkpoints(page_id TEXT NOT NULL,rev INTEGER NOT NULL,snapshot BLOB NOT NULL,blocks BLOB NOT NULL,PRIMARY KEY(page_id,rev))`,
	} {
		execOrFatal(t, h.db, statement)
	}
	execOrFatal(t, h.db, `INSERT INTO d0_documents VALUES(?,0,0,1,X'','[]')`, page)
	const secret = "d0-synthetic-auth-secret-for-isolated-test-only"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{owner, editor} {
		session, e := sessions.NewDBStore(h.db).Create(t.Context(), user, "D0", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	middleware := NewAuthMiddleware(validator, sessions.NewDBStore(h.db), h.db, newTestLogger())
	serviceBytes := make([]byte, 32)
	if _, err = rand.Read(serviceBytes); err != nil {
		t.Fatal(err)
	}
	serviceToken := hex.EncodeToString(serviceBytes)
	backupPath := filepath.Join(t.TempDir(), "d0-backup.db")
	// Every writer (including test controls) takes a SQLite writer fence before
	// resolving live session, membership, Page ACL and agent delegation.
	begin := func(ctx context.Context) (*sql.Tx, error) {
		tx, e := h.db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE pages SET updated_at=updated_at WHERE id=?`, page); e != nil {
			tx.Rollback()
			return nil, e
		}
		return tx, nil
	}
	authorize := func(r *http.Request, tx *sql.Tx, write bool) (string, error) {
		ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
		user := UserFromContext(ctx)
		if user == nil {
			return "", errors.New("unauthenticated")
		}
		role, e := h.liveContentHumanRole(ctx, workspace, user.ID)
		if e != nil {
			return "", e
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		rec, e := h.loadPage(ctx, workspace, "report")
		if e != nil {
			return "", e
		}
		if a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); ok {
			if e = h.checkArtifactRun(ctx, page, a); e != nil {
				return "", e
			}
			return "agent:" + a.AgentID + ":" + a.RunID, nil
		}
		var expiry string
		var revoked sql.NullString
		// Middleware verifies the JWT. Recheck its live DB session under fence,
		// rather than treating a previously accepted connection as authority.
		if user.SessionID == "" {
			return "", errors.New("session required")
		}
		if e = tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at FROM user_sessions WHERE id=? AND user_id=?`, user.SessionID, user.ID).Scan(&expiry, &revoked); e != nil || revoked.Valid {
			return "", errors.New("session revoked")
		}
		limit, parseErr := time.Parse(time.RFC3339Nano, expiry)
		if parseErr != nil || !time.Now().Before(limit) {
			return "", errors.New("session expired")
		}
		viewer, e := h.loadViewer(ctx, workspace, user.ID)
		if e != nil {
			return "", e
		}
		if write {
			if !h.mayEditSpec(ctx, workspace, user.ID, role, rec) {
				return "", errors.New("write revoked")
			}
		} else {
			ok, e := h.canSeePage(ctx, workspace, rec, nil, viewer)
			if e != nil || !ok {
				return "", errors.New("read revoked")
			}
		}
		return "user:" + user.ID, nil
	}
	serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		commitStarted := time.Now()
		ctx := r.Context()
		tx, e := begin(ctx)
		if e != nil {
			replyError(w, 500, e.Error())
			return
		}
		defer tx.Rollback()
		actor, e := authorize(r, tx, r.URL.Path == "/commit")
		if e != nil {
			replyError(w, 403, e.Error())
			return
		}
		s, e := d0Read(ctx, tx, page)
		if e != nil {
			replyError(w, 500, e.Error())
			return
		}
		if r.URL.Path == "/state" {
			writeJSON(w, 200, s)
			return
		}
		if r.URL.Path == "/checkpoint" {
			var snapshot, blocks []byte
			if tx.QueryRowContext(ctx, `SELECT snapshot,blocks FROM d0_checkpoints WHERE page_id=? AND rev=?`, page, r.URL.Query().Get("rev")).Scan(&snapshot, &blocks) != nil {
				replyError(w, 404, "checkpoint missing")
				return
			}
			writeJSON(w, 200, map[string]any{"snapshot": snapshot, "blocks": json.RawMessage(blocks)})
			return
		}
		if r.URL.Path == "/receipt" {
			if r.URL.Query().Get("epoch") != "" && r.URL.Query().Get("epoch") != strconv.Itoa(s.Epoch) {
				replyError(w, 409, "epoch conflict")
				return
			}
			var rev int
			var fingerprint string
			e = tx.QueryRowContext(ctx, `SELECT rev,fingerprint FROM d0_commits WHERE page_id=? AND actor=? AND request_id=?`, page, actor, r.URL.Query().Get("id")).Scan(&rev, &fingerprint)
			if errors.Is(e, sql.ErrNoRows) {
				writeJSON(w, 200, map[string]any{"found": false})
				return
			}
			if e != nil {
				replyError(w, 500, e.Error())
				return
			}
			if fingerprint != r.URL.Query().Get("fingerprint") {
				replyError(w, 409, "idempotency key reused for different request")
				return
			}
			writeJSON(w, 200, map[string]any{"found": true, "rev": rev})
			return
		}
		if r.URL.Path != "/commit" || r.Method != "POST" {
			replyError(w, 404, "unknown D0 operation")
			return
		}
		var c d0Commit
		r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
		if json.NewDecoder(r.Body).Decode(&c) != nil || len(c.ID) == 0 || len(c.ID) > 128 || len(c.Fingerprint) != 64 || len(c.Snapshot) > 1<<20 || len(c.Blocks) > 4000 {
			replyError(w, 400, "invalid bounded commit")
			return
		}
		if c.Epoch != s.Epoch {
			replyError(w, 409, "epoch conflict")
			return
		}
		// A successful retry may have an old ifRev; authority still must be live.
		var previous int
		var fingerprint string
		e = tx.QueryRowContext(ctx, `SELECT rev,fingerprint FROM d0_commits WHERE page_id=? AND actor=? AND request_id=?`, page, actor, c.ID).Scan(&previous, &fingerprint)
		if e == nil {
			if fingerprint != c.Fingerprint {
				replyError(w, 409, "idempotency key reused")
				return
			}
			writeJSON(w, 200, map[string]any{"rev": previous})
			return
		}
		if !errors.Is(e, sql.ErrNoRows) {
			replyError(w, 500, e.Error())
			return
		}
		if c.Epoch != s.Epoch || c.IfRev != s.Rev || c.Schema != s.Schema || s.Schema != 1 {
			replyError(w, 409, "epoch, revision or schema conflict")
			return
		}
		for id, hash := range c.IfHashes {
			found := false
			for _, b := range s.Blocks {
				if b.ID == id && b.Hash == hash {
					found = true
				}
			}
			if !found {
				replyError(w, 409, "target hash conflict")
				return
			}
		}
		switch c.Kind {
		case "create", "human", "agent", "checkpoint", "restore", "comment", "compact":
		default:
			replyError(w, 400, "unknown commit kind")
			return
		}
		if c.Kind == "create" && (s.Rev != 0 || len(s.Snapshot) != 0) {
			replyError(w, 409, "document already initialized")
			return
		}
		if strings.HasPrefix(actor, "agent:") && c.Kind != "agent" {
			replyError(w, 403, "agent commit kind required")
			return
		}
		if c.Kind == "restore" {
			var savedSnapshot, savedBlocks []byte
			if e = tx.QueryRowContext(ctx, `SELECT snapshot,blocks FROM d0_checkpoints WHERE page_id=? AND rev=?`, page, c.Checkpoint).Scan(&savedSnapshot, &savedBlocks); e != nil {
				replyError(w, 409, "restore checkpoint missing")
				return
			}
			if !bytes.Equal(savedSnapshot, c.Snapshot) {
				replyError(w, 409, "restore snapshot mismatch")
				return
			}
			var expected []d0Block
			if e = json.Unmarshal(savedBlocks, &expected); e != nil {
				replyError(w, 500, e.Error())
				return
			}
			actual, _ := json.Marshal(c.Blocks)
			canonical, _ := json.Marshal(expected)
			if !bytes.Equal(actual, canonical) {
				replyError(w, 409, "restore blocks mismatch")
				return
			}
		}
		blocks, e := json.Marshal(c.Blocks)
		if e != nil {
			replyError(w, 400, e.Error())
			return
		}
		s.Rev++
		if c.Kind == "restore" {
			s.Epoch++
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO d0_commits VALUES(?,?,?,?,?,?,?,?)`, page, s.Rev, actor, c.Kind, c.ID, c.Fingerprint, c.Snapshot, blocks)
		if e == nil {
			_, e = tx.ExecContext(ctx, `UPDATE d0_documents SET rev=?,epoch=?,snapshot=?,blocks=? WHERE page_id=?`, s.Rev, s.Epoch, c.Snapshot, blocks, page)
		}
		if e == nil && len(c.Thread) > 0 {
			var thread struct {
				ID string `json:"id"`
			}
			e = json.Unmarshal(c.Thread, &thread)
			if e == nil && thread.ID == "" {
				e = errors.New("thread id required")
			}
			if e == nil {
				_, e = tx.ExecContext(ctx, `INSERT INTO d0_threads VALUES(?,?,?)`, page, thread.ID, []byte(c.Thread))
			}
		}
		if e == nil && (c.Kind == "checkpoint" || c.Kind == "agent" || c.Kind == "restore") {
			_, e = tx.ExecContext(ctx, `INSERT INTO d0_checkpoints VALUES(?,?,?,?)`, page, s.Rev, c.Snapshot, blocks)
		}
		if e != nil {
			replyError(w, 500, e.Error())
			return
		}
		if e = tx.Commit(); e != nil {
			replyError(w, 500, e.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"rev": s.Rev, "epoch": s.Epoch, "commitMicros": time.Since(commitStarted).Microseconds()})
	})
	// Service identity is required in addition to each end-user/run bearer.
	// The service is trusted to validate Yjs/schema; it cannot access SQLite.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-D0-Service") != serviceToken {
			replyError(w, 401, "service identity required")
			return
		}
		if r.URL.Path == "/epoch" {
			tx, e := begin(r.Context())
			if e != nil {
				replyError(w, 500, e.Error())
				return
			}
			defer tx.Rollback()
			_, e = tx.Exec(`UPDATE d0_documents SET epoch=epoch+1 WHERE page_id=?`, page)
			if e != nil {
				replyError(w, 500, e.Error())
				return
			}
			s, e := d0Read(r.Context(), tx, page)
			if e == nil {
				e = tx.Commit()
			}
			if e != nil {
				replyError(w, 500, e.Error())
				return
			}
			writeJSON(w, 200, s)
			return
		}
		if r.URL.Path == "/control" {
			admitted := false
			r.Header.Set("X-Workspace-ID", workspace)
			middleware.RequireAuth(middleware.RequireWorkspace(http.HandlerFunc(func(_ http.ResponseWriter, checked *http.Request) { r = checked; admitted = true }))).ServeHTTP(w, r)
			if !admitted {
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+tokens[owner] {
				replyError(w, 403, "fixture owner required")
				return
			}
			action := r.URL.Query().Get("action")
			if action == "backup" {
				if _, e := h.db.ExecContext(r.Context(), `VACUUM INTO ?`, backupPath); e != nil {
					replyError(w, 500, e.Error())
					return
				}
				writeJSON(w, 200, map[string]bool{"ok": true})
				return
			}
			switch action {
			case "revoke", "grant", "revoke-agent", "schema2", "schema1", "revoke-session":
				tx, e := begin(r.Context())
				if e != nil {
					replyError(w, 500, e.Error())
					return
				}
				defer tx.Rollback()
				if _, e = authorize(r, tx, true); e != nil {
					replyError(w, 403, e.Error())
					return
				}
				switch action {
				case "revoke":
					_, e = tx.ExecContext(r.Context(), `DELETE FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, editor)
				case "grant":
					_, e = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, editor, owner)
				case "revoke-agent":
					_, e = tx.ExecContext(r.Context(), `UPDATE page_artifact_run_delegations SET revoked_at=? WHERE page_id=?`, tsformat.Format(time.Now()), page)
				case "schema2":
					_, e = tx.ExecContext(r.Context(), `UPDATE d0_documents SET schema_version=2,epoch=epoch+1 WHERE page_id=?`, page)
				case "schema1":
					_, e = tx.ExecContext(r.Context(), `UPDATE d0_documents SET schema_version=1,epoch=epoch+1 WHERE page_id=?`, page)
				case "revoke-session":
					_, e = tx.ExecContext(r.Context(), `UPDATE user_sessions SET revoked_at=? WHERE user_id=?`, tsformat.Format(time.Now()), editor)
				}
				if e == nil {
					e = tx.Commit()
				}
				if e != nil {
					replyError(w, 500, e.Error())
					return
				}

			case "restore-backup":
				backup, e := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
				if e != nil {
					replyError(w, 500, e.Error())
					return
				}
				defer backup.Close()
				bt, e := backup.Begin()
				if e != nil {
					replyError(w, 500, e.Error())
					return
				}
				saved, e := d0Read(r.Context(), bt, page)
				bt.Rollback()
				if e != nil {
					replyError(w, 500, e.Error())
					return
				}
				tx, e := begin(r.Context())
				if e != nil {
					replyError(w, 500, e.Error())
					return
				}
				defer tx.Rollback()
				if _, e = authorize(r, tx, true); e != nil {
					replyError(w, 403, e.Error())
					return
				}
				blocks, _ := json.Marshal(saved.Blocks)
				_, e = tx.Exec(`UPDATE d0_documents SET snapshot=?,blocks=?,rev=?,epoch=epoch+1,schema_version=? WHERE page_id=?`, saved.Snapshot, blocks, saved.Rev, saved.Schema, page)
				for _, table := range []string{"d0_commits", "d0_checkpoints"} {
					if e == nil {
						_, e = tx.Exec(`DELETE FROM `+table+` WHERE page_id=? AND rev>?`, page, saved.Rev)
					}
				}
				if e == nil {
					_, e = tx.Exec(`DELETE FROM d0_threads WHERE page_id=?`, page)
				}
				var threads []json.RawMessage
				_ = json.Unmarshal(saved.Threads, &threads)
				for _, raw := range threads {
					var v struct {
						ID string `json:"id"`
					}
					_ = json.Unmarshal(raw, &v)
					if e == nil {
						_, e = tx.Exec(`INSERT INTO d0_threads VALUES(?,?,?)`, page, v.ID, []byte(raw))
					}
				}
				if e == nil {
					_, e = tx.Exec(`UPDATE page_artifact_run_delegations SET revoked_at=? WHERE page_id=?`, tsformat.Format(time.Now()), page)
				}
				if e == nil {
					e = tx.Commit()
				}
				if e != nil {
					replyError(w, 500, e.Error())
					return
				}
			default:
				replyError(w, 400, "unknown control")
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer agtv2.") {
			ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
			ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
			if checked, ok := h.artifactAgentRequest(w, r.WithContext(ctx)); ok {
				serve.ServeHTTP(w, checked)
			}
			return
		}
		middleware.RequireAuth(middleware.RequireWorkspace(serve)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	repo, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	fixture, e := json.Marshal(map[string]any{"authority": server.URL, "workspace": workspace, "page": page, "owner": tokens[owner], "editor": tokens[editor], "agent": agentToken, "service": serviceToken})
	if e != nil {
		t.Fatal(e)
	}
	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	if e = os.WriteFile(fixturePath, fixture, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "tools/docs-d0/acceptance.mjs"), fixturePath)
	command.Dir = filepath.Join(repo, "tools/docs-d0")
	command.Env = os.Environ()
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if e = command.Run(); e != nil {
		t.Fatal("D0 integration/browser acceptance", e)
	}
	var n int
	if e = h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&n); e != nil || n != 0 {
		t.Fatalf("prototype touched HTML history: %d %v", n, e)
	}
	var actors int
	if e = h.db.QueryRow(`SELECT COUNT(DISTINCT actor) FROM d0_commits WHERE page_id=?`, page).Scan(&actors); e != nil || actors != 3 {
		t.Fatalf("expected two humans and an agent audited, actors=%d err=%v", actors, e)
	}
	// Exercise the Go authority directly so service-side validation cannot mask
	// a missing fence or precondition at the durable write boundary.
	readState := func() d0State {
		t.Helper()
		tx, err := h.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		state, err := d0Read(t.Context(), tx, page)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	durableBytes := func() []byte {
		t.Helper()
		tables := []struct{ name, order string }{{"d0_documents", "page_id"}, {"d0_commits", "page_id,rev"}, {"d0_threads", "page_id,id"}, {"d0_checkpoints", "page_id,rev"}}
		all := make([][][]any, 0, len(tables))
		for _, table := range tables {
			rows, err := h.db.QueryContext(t.Context(), `SELECT * FROM `+table.name+` ORDER BY `+table.order)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			values := [][]any{}
			for rows.Next() {
				row := make([]any, len(columns))
				dest := make([]any, len(columns))
				for i := range row {
					dest[i] = &row[i]
				}
				if err = rows.Scan(dest...); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				values = append(values, row)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, values)
		}
		encoded, err := json.Marshal(all)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	assertRequest := func(path, bearer, service string, payload any, status int) {
		t.Helper()
		var body io.Reader
		method := "GET"
		if payload != nil {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
			method = "POST"
		}
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-D0-Service", service)
		req.Header.Set("X-Workspace-ID", workspace)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		// Do not print request headers or response bodies containing credentials.
		if response.StatusCode != status {
			t.Fatalf("direct authority %s: status %d, want %d", path, response.StatusCode, status)
		}
	}
	last := readState()
	before := durableBytes()
	base := d0Commit{ID: "go-fence-rejection", Fingerprint: strings.Repeat("a", 64), Kind: "human", Epoch: last.Epoch, Schema: last.Schema, IfRev: last.Rev, Snapshot: last.Snapshot, Blocks: last.Blocks}
	stale := base
	stale.IfRev = last.Rev - 1
	assertRequest("/commit", tokens[owner], serviceToken, stale, 409)
	badHash := base
	badHash.IfHashes = map[string]string{"go-missing-block": strings.Repeat("f", 64)}
	assertRequest("/commit", tokens[owner], serviceToken, badHash, 409)
	oldEpoch := base
	oldEpoch.Epoch = last.Epoch - 1
	// Use a real persisted receipt: even an exact retry must respect epoch.
	if err := h.db.QueryRowContext(t.Context(), `SELECT request_id,fingerprint FROM d0_commits WHERE page_id=? AND actor=? ORDER BY rev DESC LIMIT 1`, page, "user:"+owner).Scan(&oldEpoch.ID, &oldEpoch.Fingerprint); err != nil {
		t.Fatal(err)
	}
	assertRequest("/commit", tokens[owner], serviceToken, oldEpoch, 409)
	var revokedDelegations int
	if err := h.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM page_artifact_run_delegations WHERE page_id=? AND revoked_at IS NOT NULL`, page).Scan(&revokedDelegations); err != nil || revokedDelegations == 0 {
		t.Fatalf("backup restore must revoke agent delegation: count=%d err=%v", revokedDelegations, err)
	}
	agentCommit := base
	agentCommit.Kind = "agent"
	assertRequest("/commit", agentToken, serviceToken, agentCommit, 403)
	assertRequest("/commit", tokens[owner], "", base, 401)
	assertRequest("/commit", tokens[owner], "invalid-service", base, 401)
	if !bytes.Equal(before, durableBytes()) {
		t.Fatal("rejected direct authority requests changed D0 durable tables")
	}
	claims, err := validator.ValidateAccess(tokens[owner])
	if err != nil {
		t.Fatal(err)
	}
	tx, err := begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), `UPDATE user_sessions SET revoked_at=? WHERE id=? AND user_id=?`, tsformat.Format(time.Now()), claims.Sid, owner); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertRequest("/state", tokens[owner], serviceToken, nil, 401)
	if !bytes.Equal(before, durableBytes()) {
		t.Fatal("revoked session read changed D0 durable tables")
	}

}
