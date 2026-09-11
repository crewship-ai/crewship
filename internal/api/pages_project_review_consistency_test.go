package api

// Pages — what the review snapshot actually guarantees about concurrency
// (docs/prd/pages.md §11).
//
// The claim under test, as the endpoint's own comments put it, is that the
// live definition, the candidate definition and `definition_digest` describe
// "one instant of one row" — i.e. that the snapshot is a point-in-time view of
// the database.
//
// One handler is not one database snapshot. ReviewProject issues roughly ten
// separate statements on `h.db` with no `BeginTx` anywhere in it, so each one
// runs in its own implicit SQLite read transaction. `_txlock=immediate` in the
// DSN (internal/database/database.go) governs explicit transactions only; it
// says nothing about autocommit reads. Under WAL each of those reads takes a
// fresh snapshot at statement time, so a committed write can — and here,
// demonstrably does — land between two of them.
//
// These tests separate two guarantees that the prose blurs:
//
//   - PER-READ CONSISTENCY. Values taken from ONE statement agree with each
//     other. `baseline.definition` and `baseline.definition_digest` come from
//     one `SELECT spec_json FROM pages`; `candidate.{revision,git_commit,
//     source_digest,definition}` come from one `SELECT ... FROM
//     page_project_drafts`. That holds, and it is what "two documents out of
//     one handler cannot drift" is really about.
//
//   - CROSS-READ ATOMICITY. Values from DIFFERENT statements describe one
//     instant. That does NOT hold, and TestPageProjectReviewSnapshotIsNotOne
//     DatabaseSnapshot shows the snapshot reporting a combination of rows that
//     no single-transaction reader could ever have seen.
//
// What makes the screen's consent sound is therefore not the read. It is the
// publish fence: PublishProject re-reads the live definition, the draft row
// and every called routine INSIDE the writing transaction and compares them
// with the values the caller attested to. A stale or mixed snapshot produces a
// 409, never a silent publication. Every test below that ends in a conflict is
// naming the fence as the thing doing the work.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

// ── The seam ───────────────────────────────────────────────────────────────
//
// ReviewProject holds a `*sql.DB` (a concrete type), so there is no interface
// to stub and no hook to inject without editing the handler. What database/sql
// does expose is the driver: a connection that implements neither
// driver.QueryerContext nor driver.ExecerContext forces every statement
// through PrepareContext, where the SQL text is visible. Wrapping the real
// SQLite connection that way gives a deterministic "run this write immediately
// before the handler's Nth statement matching X" seam that lives entirely in
// this file.
//
// Deterministic, not racy: the interleaved write runs on the test goroutine,
// synchronously, at a named point in the handler's statement sequence. There
// is no sleep and no second goroutine to lose a race with.

type reviewQueryHook struct {
	mu    sync.Mutex
	match string
	nth   int
	seen  int
	fired bool
	run   func()
}

// before runs the interleaved write just before the Nth statement whose SQL
// contains `match` is prepared. The callback writes through a DIFFERENT
// *sql.DB, so it cannot re-enter this hook.
func (k *reviewQueryHook) before(query string) {
	k.mu.Lock()
	if k.run == nil || k.fired || !strings.Contains(query, k.match) {
		k.mu.Unlock()
		return
	}
	k.seen++
	if k.seen != k.nth {
		k.mu.Unlock()
		return
	}
	k.fired = true
	run := k.run
	k.mu.Unlock()
	run()
}

func (k *reviewQueryHook) didFire() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.fired
}

type reviewHookConnector struct {
	dsn   string
	inner driver.Driver
	hook  *reviewQueryHook
}

func (c *reviewHookConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.inner.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &reviewHookConn{inner: conn, hook: c.hook}, nil
}

func (c *reviewHookConnector) Driver() driver.Driver { return c.inner }

// reviewHookConn deliberately implements only Prepare/PrepareContext/Close/
// Begin/BeginTx. Omitting QueryerContext and ExecerContext is what routes
// every statement through PrepareContext; BeginTx is forwarded so the DSN's
// `_txlock=immediate` keeps applying to explicit transactions.
type reviewHookConn struct {
	inner driver.Conn
	hook  *reviewQueryHook
}

func (c *reviewHookConn) Prepare(query string) (driver.Stmt, error) {
	c.hook.before(query)
	return c.inner.Prepare(query)
}

func (c *reviewHookConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.hook.before(query)
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.inner.Prepare(query)
}

func (c *reviewHookConn) Close() error { return c.inner.Close() }

func (c *reviewHookConn) Begin() (driver.Tx, error) { return c.inner.Begin() } //nolint:staticcheck // driver.Conn requires it.

func (c *reviewHookConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.inner.Begin() //nolint:staticcheck // fallback for a driver without ConnBeginTx.
}

// reviewDBPath is the file the handler's pool is open on, asked of SQLite
// itself rather than threaded down from the fixture.
func reviewDBPath(t *testing.T, db *sql.DB) string {
	t.Helper()
	var path string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatalf("read database path: %v", err)
	}
	if path == "" {
		t.Fatal("the test database is not a file, so a second handle cannot be opened on it")
	}
	return path
}

// reviewHookedDB opens a second pool on the same file through the wrapping
// connector. The pragmas are the semantically load-bearing subset of the ones
// internal/database/database.go sets — journal mode is a property of the file
// and is already WAL, `_txlock=immediate` is what makes an explicit
// transaction take the write lock at BEGIN, and foreign keys are per
// connection. cache_size/mmap_size are performance knobs and are left out.
func reviewHookedDB(t *testing.T, path string, hook *reviewQueryHook) *sql.DB {
	t.Helper()
	probe, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open probe handle: %v", err)
	}
	base := probe.Driver()
	if err := probe.Close(); err != nil {
		t.Fatalf("close probe handle: %v", err)
	}
	dsn := path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate"
	db := sql.OpenDB(&reviewHookConnector{dsn: dsn, inner: base, hook: hook})
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping hooked handle: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// reviewInterleaved calls ReviewProject with `write` committed, on another
// connection, immediately before the handler's `nth` statement whose SQL
// contains `match`.
//
// It swaps h.db only for the duration of the call, so the fixture, the build
// worker and the interleaved write all keep using the production handle that
// internal/database.Open produced.
func reviewInterleaved(t *testing.T, h *PageHandler, ws, actor, role, slug, match string, nth int, write func()) (*httptest.ResponseRecorder, reviewSnapshotWire) {
	t.Helper()
	hook := &reviewQueryHook{match: match, nth: nth, run: write}
	production := h.db
	hooked := reviewHookedDB(t, reviewDBPath(t, production), hook)
	h.db = hooked
	defer func() { h.db = production }()
	w, snapshot := reviewCall(t, h, ws, actor, role, slug)
	if !hook.didFire() {
		t.Fatalf("the interleaved write never ran: no %dth statement matching %q was prepared, so this test proved nothing", nth, match)
	}
	return w, snapshot
}

// ── Fixtures ───────────────────────────────────────────────────────────────

// reviewStoredSpec is the live Page declaration as stored, whoever is asking.
func reviewStoredSpec(t *testing.T, h *PageHandler, slug string) string {
	t.Helper()
	var spec string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE slug=?`, slug).Scan(&spec); err != nil {
		t.Fatalf("read stored definition: %v", err)
	}
	return spec
}

func reviewDigestOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// reviewRenamePanel returns the given stored document with one panel title
// changed, which moves the stored bytes without making the document invalid.
func reviewRenamePanel(t *testing.T, spec, title string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatalf("stored definition is not JSON: %v", err)
	}
	specObject, _ := doc["spec"].(map[string]any)
	panels, _ := specObject["panels"].([]any)
	if len(panels) == 0 {
		t.Fatal("the fixture document has no panel to rename")
	}
	first, _ := panels[0].(map[string]any)
	first["title"] = title
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// reviewSourcedFixture is a Page with a source project saved (draft at
// revision 1) and everything a publication needs configured. It is
// reviewFixture plus the one PUT every test here starts from.
func reviewSourcedFixture(t *testing.T) (*PageHandler, string, string) {
	t.Helper()
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatalf("seed project sources: %d %s", w.Code, w.Body.String())
	}
	return h, ws, user
}

// reviewPublishedFixture is a Page with sources, a built draft and one live
// publication, i.e. the state in which every base the fence guards exists.
func reviewPublishedFixture(t *testing.T) (*PageHandler, string, string) {
	t.Helper()
	h, ws, user := reviewSourcedFixture(t)
	build := reviewBuildRevision(t, h, ws, user, 1)
	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true,
	}); w.Code != 200 {
		t.Fatalf("seed publication: %d %s", w.Code, w.Body.String())
	}
	return h, ws, user
}

// reviewConflictKind is the `conflict` discriminator on a 409 body.
func reviewConflictKind(t *testing.T, w *httptest.ResponseRecorder) (string, []string) {
	t.Helper()
	var body struct {
		Conflict string   `json:"conflict"`
		Routines []string `json:"routines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("conflict body is not JSON: %v — %s", err, w.Body.String())
	}
	return body.Conflict, body.Routines
}

// ── What the DSN actually buys ─────────────────────────────────────────────

// TestPageDatabaseAutocommitReadsAreSeparateSnapshots is the premise every
// other test in this file rests on, asserted rather than assumed.
//
// `_txlock=immediate` makes an EXPLICIT transaction take SQLite's write lock
// at BEGIN — that is what makes the publish fence's read-then-write atomic. It
// does nothing at all for the statements a handler issues outside a
// transaction, which is all of ReviewProject's.
func TestPageDatabaseAutocommitReadsAreSeparateSnapshots(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	pagesCreate(t, h, ws, user, "health")

	t.Run("two autocommit reads bracket a committed write", func(t *testing.T) {
		if _, err := h.db.Exec(`UPDATE pages SET name='before'`); err != nil {
			t.Fatal(err)
		}
		var first, second string
		if err := h.db.QueryRow(`SELECT COALESCE(MAX(name),'') FROM pages`).Scan(&first); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(`UPDATE pages SET name='after'`); err != nil {
			t.Fatal(err)
		}
		if err := h.db.QueryRow(`SELECT COALESCE(MAX(name),'') FROM pages`).Scan(&second); err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Fatalf("two autocommit reads returned the same value (%q) across a committed write — "+
				"if this ever holds, the premise of every interleaving test here is wrong", first)
		}
	})

	t.Run("the Page storage lease is shared and serialises nothing", func(t *testing.T) {
		// pageLease takes ProjectStore.Lease(ws, exclusive=false). Two shared
		// flocks coexist, so review, save and publish never exclude one
		// another — nothing in the request path serialises them and the fence
		// is the only thing standing between them.
		store := &pages.ProjectStore{Directory: storageDir(t)}
		first, err := store.Lease(context.Background(), ws, false)
		if err != nil {
			t.Fatalf("first shared lease: %v", err)
		}
		defer first()
		second, err := store.Lease(context.Background(), ws, false)
		if err != nil {
			t.Fatalf("a second shared Page storage lease was refused (%v); if the lease has become exclusive, "+
				"review and publish now serialise and these tests describe the wrong world", err)
		}
		second()
	})

	t.Run("an explicit transaction does hold the write lock from BEGIN", func(t *testing.T) {
		path := reviewDBPath(t, h.db)
		other, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(200)&_txlock=immediate")
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		tx, err := h.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck // the test never commits it.
		var spec string
		if err := tx.QueryRow(`SELECT COALESCE(MAX(spec_json),'') FROM pages`).Scan(&spec); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if _, err := other.ExecContext(ctx, `UPDATE pages SET name='stolen'`); err == nil {
			t.Error("another connection committed a write while a BEGIN IMMEDIATE transaction was open; " +
				"the publish fence's read-then-compare-then-write would not be atomic")
		}
	})
}

// ── Claim 1: the baseline document and its digest ──────────────────────────

// TestPageProjectReviewBaselineDefinitionAndDigestComeFromOneRead — claim 1.
//
// `definition_digest` is computed the instant `SELECT spec_json FROM pages` in
// ReviewProject returns; `baseline.definition` is derived from the same local
// variable several statements later, after the publication rows, the draft row
// and the candidate's build have been read. If it were re-read there instead,
// a write landing in between would make the two halves of the snapshot
// describe different documents — the exact correspondence failure the fields
// were added to close.
//
// The write here lands after the live definition is read and before the draft
// is, so it is inside that window. The digest and the document must both still
// be the pre-write bytes.
func TestPageProjectReviewBaselineDefinitionAndDigestComeFromOneRead(t *testing.T) {
	h, ws, user := reviewSourcedFixture(t)
	before := reviewStoredSpec(t, h, "health")
	after := reviewRenamePanel(t, before, "Moved mid-handler")
	if before == after {
		t.Fatal("the interleaved write does not change the stored bytes, so this test proves nothing")
	}

	_, snapshot := reviewInterleaved(t, h, ws, user, "OWNER", "health", "FROM page_project_drafts WHERE page_id=?", 1, func() {
		if _, err := h.db.Exec(`UPDATE pages SET spec_json=? WHERE slug='health'`, after); err != nil {
			t.Errorf("interleaved write: %v", err)
		}
	})

	if got := reviewStoredSpec(t, h, "health"); got != after {
		t.Fatalf("the interleaved write did not stick, so the window was never exercised")
	}
	if snapshot.Baseline.DefinitionDigest != reviewDigestOf(before) {
		t.Errorf("definition_digest = %s, want the digest of the bytes read at the top of the handler (%s)",
			snapshot.Baseline.DefinitionDigest, reviewDigestOf(before))
	}
	if got := string(snapshot.Baseline.Definition); got != before {
		t.Errorf("baseline.definition is not the document the digest was taken over.\n got: %s\nwant: %s", got, before)
	}
	// The owner is withheld nothing, so the served document IS the hashed
	// bytes and a client can verify the correspondence for itself.
	if reviewDigestOf(string(snapshot.Baseline.Definition)) != snapshot.Baseline.DefinitionDigest {
		t.Errorf("sha256(baseline.definition) = %s but definition_digest = %s: the two came from different reads",
			reviewDigestOf(string(snapshot.Baseline.Definition)), snapshot.Baseline.DefinitionDigest)
	}
	// ...and the snapshot as a whole is nonetheless STALE. That is the true
	// shape of the guarantee: internally consistent, not current.
	if snapshot.Baseline.DefinitionDigest == reviewDigestOf(after) {
		t.Fatal("the snapshot reported the post-write digest, so the interleaving did not land where this test needs it")
	}
}

// TestPageProjectReviewStaleBaselineDigestCannotPublish is the other half of
// claim 1, and the reason the staleness above is not a defect.
//
// The consent a reviewer gives is carried by `expected_definition_digest`. A
// digest that was current when the snapshot was built and is not current when
// the publication arrives is refused inside the writing transaction.
func TestPageProjectReviewStaleBaselineDigestCannotPublish(t *testing.T) {
	h, ws, user := reviewSourcedFixture(t)
	build := reviewBuildRevision(t, h, ws, user, 1)
	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Candidate == nil {
		t.Fatal("the fixture has a draft, so there must be a candidate")
	}
	stored := reviewStoredSpec(t, h, "health")
	if _, err := h.db.Exec(`UPDATE pages SET spec_json=? WHERE slug='health'`, reviewRenamePanel(t, stored, "Moved after review")); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: snapshot.Candidate.Revision, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
	})
	if w.Code != 409 {
		t.Fatalf("publishing against a digest the live definition has moved past returned %d %s, want 409", w.Code, w.Body.String())
	}
	if kind, _ := reviewConflictKind(t, w); kind != "definition" {
		t.Errorf("conflict = %q, want \"definition\": the caller has to be told WHICH base moved", kind)
	}
}

// ── Claim 2: the candidate tuple ───────────────────────────────────────────

// TestPageProjectReviewCandidateIsOneDraftRow — claim 2.
//
// `candidate.revision`, `candidate.git_commit`, `candidate.source_digest` and
// `candidate.definition` are four columns of ONE `SELECT ... FROM
// page_project_drafts`, and PutProject moves all four in one transaction. A
// save landing after that statement therefore cannot split them: the snapshot
// reports the whole old tuple or the whole new one, never a mix.
//
// The write lands between the draft read and the revision-metadata read that
// follows it, which is the only window in which a second read of the draft
// could have produced a mixed tuple.
func TestPageProjectReviewCandidateIsOneDraftRow(t *testing.T) {
	h, ws, user := reviewSourcedFixture(t)
	var revision int64
	var digest, commit, spec string
	if err := h.db.QueryRow(`SELECT d.revision,d.source_digest,d.git_commit,d.spec_json FROM page_project_drafts d JOIN pages p ON p.id=d.page_id WHERE p.slug='health'`).
		Scan(&revision, &digest, &commit, &spec); err != nil {
		t.Fatal(err)
	}

	_, snapshot := reviewInterleaved(t, h, ws, user, "OWNER", "health", "FROM page_project_revisions WHERE page_id=? AND revision=?", 1, func() {
		// Exactly the shape PutProject commits: revision, digest, commit and
		// document all move together, plus the append-only revision row.
		next := reviewRenamePanel(t, spec, "Saved mid-handler")
		if _, err := h.db.Exec(`UPDATE page_project_drafts SET revision=revision+1,source_digest='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',git_commit='commit-interleaved',spec_json=?
			WHERE page_id=(SELECT id FROM pages WHERE slug='health')`, next); err != nil {
			t.Errorf("interleaved draft save: %v", err)
			return
		}
		if _, err := h.db.Exec(`INSERT INTO page_project_revisions(page_id,revision,source_digest,actor_user_id,created_at,git_commit,spec_json)
			SELECT id,?,'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',?, '2026-09-11T00:00:00Z','commit-interleaved',? FROM pages WHERE slug='health'`,
			revision+1, user, next); err != nil {
			t.Errorf("interleaved revision row: %v", err)
		}
	})

	if snapshot.Candidate == nil {
		t.Fatal("no candidate on the snapshot")
	}
	got := snapshot.Candidate
	if got.Revision != revision || got.GitCommit != commit || got.SourceDigest != digest {
		t.Errorf("candidate tuple is mixed: revision=%d git_commit=%q source_digest=%q, want %d/%q/%q — all four "+
			"columns come from one statement and a save moves all four at once",
			got.Revision, got.GitCommit, got.SourceDigest, revision, commit, digest)
	}
	if string(got.Definition) != spec {
		t.Errorf("candidate.definition is not the document belonging to revision %d.\n got: %s\nwant: %s", got.Revision, got.Definition, spec)
	}
	var now int64
	if err := h.db.QueryRow(`SELECT d.revision FROM page_project_drafts d JOIN pages p ON p.id=d.page_id WHERE p.slug='health'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if now != revision+1 {
		t.Fatalf("the interleaved save did not stick (draft revision %d), so the window was never exercised", now)
	}
}

// ── Claim 3: the sources the screen loaded ─────────────────────────────────

// TestPageProjectReviewSourcesCorrespondToThePublishedCandidate — claim 3.
//
// The editor reads the application's files from `GET .../project`, which
// serves the draft's source bundle content-addressed by
// `page_project_drafts.source_digest`. Nothing about that read is pinned to
// the review snapshot, so the correspondence is not established by the read at
// all — it is established at publish time, where `checkPageCandidate` refuses
// unless the build's recorded source digest matches the bytes the retained Git
// checkpoint reads back, and the draft CAS refuses unless the draft is still
// on the revision the caller reviewed.
//
// This test states the correspondence that holds and names what enforces it.
func TestPageProjectReviewSourcesCorrespondToThePublishedCandidate(t *testing.T) {
	h, ws, user := reviewSourcedFixture(t)

	r := pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.GetProject(w, r)
	if w.Code != 200 {
		t.Fatalf("read project sources: %d %s", w.Code, w.Body.String())
	}
	var loaded pageProjectDraft
	if err := json.Unmarshal(w.Body.Bytes(), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Project == nil || len(loaded.Project.Files) == 0 {
		t.Fatal("the screen loaded no source files, so there is no correspondence to test")
	}

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Candidate == nil {
		t.Fatal("no candidate on the snapshot")
	}
	if snapshot.Candidate.Revision != loaded.Revision || snapshot.Candidate.SourceDigest != loaded.Digest || snapshot.Candidate.GitCommit != loaded.GitCommit {
		t.Fatalf("the snapshot describes revision %d/%s/%s but the screen loaded sources for %d/%s/%s",
			snapshot.Candidate.Revision, snapshot.Candidate.SourceDigest, snapshot.Candidate.GitCommit,
			loaded.Revision, loaded.Digest, loaded.GitCommit)
	}

	build := reviewBuildRevision(t, h, ws, user, loaded.Revision)
	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: snapshot.Candidate.Revision, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
	}); w.Code != 200 {
		t.Fatalf("publish the reviewed candidate: %d %s", w.Code, w.Body.String())
	}
	var publishedRevision int64
	var publishedDigest, publishedCommit, publishedSpec string
	if err := h.db.QueryRow(`SELECT source_revision,source_digest,git_commit,spec_json FROM page_project_publications
		WHERE page_id=(SELECT id FROM pages WHERE slug='health') ORDER BY version DESC LIMIT 1`).
		Scan(&publishedRevision, &publishedDigest, &publishedCommit, &publishedSpec); err != nil {
		t.Fatal(err)
	}
	if publishedRevision != loaded.Revision || publishedDigest != loaded.Digest || publishedCommit != loaded.GitCommit {
		t.Errorf("the publication records revision %d/%s/%s; the reviewer read %d/%s/%s",
			publishedRevision, publishedDigest, publishedCommit, loaded.Revision, loaded.Digest, loaded.GitCommit)
	}
	if publishedSpec != string(snapshot.Candidate.Definition) {
		t.Errorf("the publication archived a different declaration than the candidate the screen showed.\n got: %s\nwant: %s",
			publishedSpec, snapshot.Candidate.Definition)
	}
}

// TestPageProjectReviewSourcesMovedAfterLoadCannotPublish is what actually
// enforces claim 3. The screen's source read is an ordinary GET with no fence
// of its own; a save between it and the publication is caught by the draft
// CAS inside the publishing transaction.
func TestPageProjectReviewSourcesMovedAfterLoadCannotPublish(t *testing.T) {
	h, ws, user := reviewSourcedFixture(t)
	build := reviewBuildRevision(t, h, ws, user, 1)
	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Candidate == nil {
		t.Fatal("no candidate on the snapshot")
	}

	source := projectTestSource()
	source.Files[3].Content = "// a second author saved while the review was open\n"
	if w := projectPut(t, h, ws, user, "OWNER", "health", 1, source); w.Code != 200 {
		t.Fatalf("concurrent save: %d %s", w.Code, w.Body.String())
	}

	zero := int64(0)
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: snapshot.Candidate.Revision, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
	})
	if w.Code != 409 {
		t.Fatalf("publishing sources the draft has moved past returned %d %s, want 409", w.Code, w.Body.String())
	}
	if kind, _ := reviewConflictKind(t, w); kind != "draft" {
		t.Errorf("conflict = %q, want \"draft\"", kind)
	}
}

// ── Claim 4: every base that moves produces a conflict ─────────────────────

// TestPageProjectPublishFencesEveryBaseTheReviewShowed — claim 4, one case per
// base the snapshot reports. Each is a change committed AFTER a real review
// snapshot was taken and BEFORE the publication that quotes it.
func TestPageProjectPublishFencesEveryBaseTheReviewShowed(t *testing.T) {
	t.Run("the live definition", func(t *testing.T) {
		h, ws, user := reviewSourcedFixture(t)
		build := reviewBuildRevision(t, h, ws, user, 1)
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		stored := reviewStoredSpec(t, h, "health")
		if _, err := h.db.Exec(`UPDATE pages SET spec_json=? WHERE slug='health'`, reviewRenamePanel(t, stored, "Panel renamed by another author")); err != nil {
			t.Fatal(err)
		}
		zero := int64(0)
		w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true,
			ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
		})
		if kind, _ := reviewConflictKind(t, w); w.Code != 409 || kind != "definition" {
			t.Fatalf("live definition moved: %d %s (conflict %q), want 409 definition", w.Code, w.Body.String(), kind)
		}
	})

	t.Run("a routine the candidate calls", func(t *testing.T) {
		h, ws, user := reviewSourcedFixture(t)
		if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-a', ?, 'ops-alpha', 'Alpha', '{"steps":[]}', 'h1')`, ws); err != nil {
			t.Fatal(err)
		}
		reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-alpha"))
		build := reviewBuildRevision(t, h, ws, user, 2)
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		fence := reviewFenceFromSnapshot(t, snapshot)
		if len(fence) != 1 {
			t.Fatalf("the candidate must fence exactly one routine, got %v", fence)
		}
		if _, err := h.db.Exec(`UPDATE pipelines SET definition_json='{"steps":[{"run":"rm -rf /"}]}' WHERE slug='ops-alpha'`); err != nil {
			t.Fatal(err)
		}
		zero := int64(0)
		w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			BuildID: build, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
			ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest, ExpectedRoutineDigests: fence,
		})
		kind, routines := reviewConflictKind(t, w)
		if w.Code != 409 || kind != "routines" {
			t.Fatalf("routine moved: %d %s (conflict %q), want 409 routines", w.Code, w.Body.String(), kind)
		}
		if len(routines) != 1 || routines[0] != "ops-alpha" {
			t.Errorf("conflict names %v, want [ops-alpha]: the reviewer has to be told which script to re-read", routines)
		}
	})

	t.Run("the draft revision", func(t *testing.T) {
		h, ws, user := reviewSourcedFixture(t)
		build := reviewBuildRevision(t, h, ws, user, 1)
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		source := projectTestSource()
		source.Files[3].Content = "// moved\n"
		if w := projectPut(t, h, ws, user, "OWNER", "health", 1, source); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		zero := int64(0)
		w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true,
			ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
		})
		if kind, _ := reviewConflictKind(t, w); w.Code != 409 || kind != "draft" {
			t.Fatalf("draft moved: %d %s (conflict %q), want 409 draft", w.Code, w.Body.String(), kind)
		}
	})

	t.Run("the publication counter", func(t *testing.T) {
		h, ws, user := reviewSourcedFixture(t)
		first := reviewBuildRevision(t, h, ws, user, 1)
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		zero := int64(0)
		if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			BuildID: first, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true,
			ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
		}); w.Code != 200 {
			t.Fatalf("first publication: %d %s", w.Code, w.Body.String())
		}
		// A second reviewer, still holding `expected_publication: 0`, with a
		// different build so this is a new publication and not an idempotent
		// re-delivery of the one above.
		source := projectTestSource()
		source.Files[3].Content = "// a second candidate\n"
		if w := projectPut(t, h, ws, user, "OWNER", "health", 1, source); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		second := reviewBuildRevision(t, h, ws, user, 2)
		w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			BuildID: second, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
		})
		if kind, _ := reviewConflictKind(t, w); w.Code != 409 || kind != "publication" {
			t.Fatalf("publication counter moved: %d %s (conflict %q), want 409 publication", w.Code, w.Body.String(), kind)
		}
	})
}

// ── Claim 5: reads taken at different moments ──────────────────────────────

// TestPageProjectReviewSnapshotIsNotOneDatabaseSnapshot refutes the wording
// directly, and is the test the endpoint's comments would have to survive.
//
// PublishProject commits, in ONE transaction, a new `page_project_publications`
// row, a new `page_project_live` pointer and the new `pages.spec_json`. Any
// reader inside a transaction therefore sees either all of the old state or
// all of the new one. ReviewProject reads `pages.spec_json` in one statement
// and the live publication in another, so a publication committing between
// them leaves it reporting the OLD live declaration beside the NEW
// publication — a combination that never existed in the database at any
// instant, and which makes it raise `definition_moved` about a drift that is
// not there.
//
// The point is not that the blocker is wrong. It is that "one instant of one
// row" is a claim about the whole snapshot that the code does not make good on.
func TestPageProjectReviewSnapshotIsNotOneDatabaseSnapshot(t *testing.T) {
	h, ws, user := reviewPublishedFixture(t)
	liveBefore := reviewStoredSpec(t, h, "health")
	var publishedBefore string
	if err := h.db.QueryRow(`SELECT p.spec_json FROM page_project_live l JOIN page_project_publications p ON p.page_id=l.page_id AND p.version=l.version
		WHERE l.page_id=(SELECT id FROM pages WHERE slug='health')`).Scan(&publishedBefore); err != nil {
		t.Fatal(err)
	}
	if liveBefore != publishedBefore {
		t.Fatalf("the fixture already drifted, so the divergence flag would prove nothing")
	}
	// A control run: with no interleaving, the two rows agree and the flag
	// stays false. Asserted on the flag rather than on a blocker — a standing
	// divergence between the live definition and the published one is
	// advisory now, and `definition_moved` is reserved for the publish fence
	// tripping. The two had shared a name and one had inherited the other's
	// behaviour.
	if _, snapshot := reviewCall(t, h, ws, user, "OWNER", "health"); snapshot.Baseline.DefinitionDiverged {
		t.Fatalf("the fixture reports a diverged definition before any interleaving, so the assertion below is not about the window")
	}

	moved := reviewRenamePanel(t, liveBefore, "Republished by the second publication")
	_, snapshot := reviewInterleaved(t, h, ws, user, "OWNER", "health", "FROM page_project_live l JOIN page_project_publications", 1, func() {
		// Exactly what PublishProject commits, and atomically: publication 2,
		// the live pointer, and the live declaration.
		tx, err := h.db.Begin()
		if err != nil {
			t.Errorf("interleaved publication: %v", err)
			return
		}
		defer tx.Rollback() //nolint:errcheck // committed below on the happy path.
		for _, step := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,actor_user_id,created_at)
			  SELECT page_id,version+1,build_id,source_revision,source_digest,git_commit,artifact_digest,?,checks_json,actor_user_id,'2026-09-11T00:00:01Z'
			  FROM page_project_publications WHERE page_id=(SELECT id FROM pages WHERE slug='health') ORDER BY version DESC LIMIT 1`, []any{moved}},
			{`UPDATE page_project_live SET version=version+1 WHERE page_id=(SELECT id FROM pages WHERE slug='health')`, nil},
			{`UPDATE pages SET spec_json=? WHERE slug='health'`, []any{moved}},
		} {
			if _, err := tx.Exec(step.query, step.args...); err != nil {
				t.Errorf("interleaved publication step: %v", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("interleaved publication commit: %v", err)
		}
	})

	if snapshot.Baseline.PublicationVersion != 2 {
		t.Fatalf("the interleaved publication did not land before the publication read (baseline version %d), "+
			"so this test did not exercise the window", snapshot.Baseline.PublicationVersion)
	}
	if string(snapshot.Baseline.Definition) != liveBefore {
		t.Fatalf("the live definition on the snapshot is not the pre-write one, so the window was not the one intended")
	}
	if !snapshot.Baseline.DefinitionDiverged {
		t.Fatal("expected the mixed snapshot to report definition_diverged: publication 2 archived a declaration the " +
			"snapshot's own (older) live definition does not match. If this ever stops holding, the reads have " +
			"become atomic and the comments in pages_project_review.go can be taken literally")
	}
	// The state the snapshot reports never existed: publication 2 and the
	// pre-publication live declaration were never both current.
	t.Log("review snapshot reported publication version 2 alongside the live definition that preceded it — " +
		"the reads are not one database snapshot")
}

// TestPageProjectReviewRoutinesReadOneAtATimeCannotSilentlyApprove — claim 5,
// routines half.
//
// `reviewRoutines` issues one `SELECT definition_json FROM pipelines` per
// routine, in sorted order, each in its own implicit transaction. Two routines
// changed in one transaction can therefore be reported half-old and half-new,
// which is again a state that never existed. What stops that from mattering is
// that the fence recomputes BOTH digests inside the publishing transaction and
// refuses the one the reviewer's map no longer matches.
func TestPageProjectReviewRoutinesReadOneAtATimeCannotSilentlyApprove(t *testing.T) {
	h, ws, user := reviewSourcedFixture(t)
	for _, q := range []string{
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-a', ?, 'ops-alpha', 'Alpha', '{"steps":["a"]}', 'h1')`,
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-b', ?, 'ops-bravo', 'Bravo', '{"steps":["b"]}', 'h2')`,
	} {
		if _, err := h.db.Exec(q, ws); err != nil {
			t.Fatal(err)
		}
	}
	reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-alpha", "ops-bravo"))
	build := reviewBuildRevision(t, h, ws, user, 2)

	alphaBefore := pageRoutineDigest(`{"steps":["a"]}`)
	// The write lands between the two `pipelines` reads: ops-alpha sorts
	// first and has already been read, ops-bravo has not.
	_, snapshot := reviewInterleaved(t, h, ws, user, "OWNER", "health", "FROM pipelines WHERE workspace_id=? AND slug=?", 2, func() {
		tx, err := h.db.Begin()
		if err != nil {
			t.Errorf("interleaved routine edit: %v", err)
			return
		}
		defer tx.Rollback() //nolint:errcheck // committed below on the happy path.
		if _, err := tx.Exec(`UPDATE pipelines SET definition_json='{"steps":["a","moved"]}' WHERE slug='ops-alpha'`); err != nil {
			t.Errorf("interleaved routine edit: %v", err)
			return
		}
		if _, err := tx.Exec(`UPDATE pipelines SET definition_json='{"steps":["b","moved"]}' WHERE slug='ops-bravo'`); err != nil {
			t.Errorf("interleaved routine edit: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("interleaved routine commit: %v", err)
		}
	})

	fence := reviewFenceFromSnapshot(t, snapshot)
	if fence["ops-alpha"] != alphaBefore {
		t.Fatalf("ops-alpha was read after the interleaved write (digest %s), so this test did not exercise the split", fence["ops-alpha"])
	}
	if fence["ops-bravo"] != pageRoutineDigest(`{"steps":["b","moved"]}`) {
		t.Fatalf("ops-bravo was read before the interleaved write (digest %s), so this test did not exercise the split", fence["ops-bravo"])
	}
	// Half-old, half-new: a pair of digests that were never simultaneously
	// current. The fence is what keeps that from becoming a publication.
	zero := int64(0)
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest, ExpectedRoutineDigests: fence,
	})
	kind, routines := reviewConflictKind(t, w)
	if w.Code != 409 || kind != "routines" {
		t.Fatalf("a mixed routine snapshot published with %d %s (conflict %q), want 409 routines", w.Code, w.Body.String(), kind)
	}
	if len(routines) != 1 || routines[0] != "ops-alpha" {
		t.Errorf("conflict names %v, want [ops-alpha] — the half of the pair the reviewer's map still describes as old", routines)
	}
}

// TestPageProjectReviewStaleRoutineSnapshotCannotPublish is the plainer case:
// the routine moves after the whole snapshot, not inside it.
func TestPageProjectReviewStaleRoutineSnapshotCannotPublish(t *testing.T) {
	h, ws, user := reviewSourcedFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-a', ?, 'ops-alpha', 'Alpha', '{"steps":[]}', 'h1')`, ws); err != nil {
		t.Fatal(err)
	}
	reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-alpha"))
	build := reviewBuildRevision(t, h, ws, user, 2)
	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	fence := reviewFenceFromSnapshot(t, snapshot)

	// Deleting the routine entirely is the sharper case: the fence has to
	// refuse rather than quietly publish a candidate whose `call` action now
	// resolves to nothing.
	if _, err := h.db.Exec(`UPDATE pipelines SET deleted_at='2026-09-11T00:00:00Z' WHERE slug='ops-alpha'`); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest, ExpectedRoutineDigests: fence,
	})
	if w.Code == 200 {
		t.Fatalf("a candidate calling a deleted routine published successfully: %s", w.Body.String())
	}
	if w.Code/100 != 4 {
		t.Errorf("publishing a candidate whose routine is gone returned %d %s, want a refusal naming the routine", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ops-alpha") {
		t.Errorf("the refusal does not name the routine to restore: %s", w.Body.String())
	}
	// Which refusal, precisely. checkPageCandidate's own 422
	// (pageUnresolvedRoutineMessage) never runs for a routine deleted before
	// the request: resolveReferences -> resolveActionRoutines
	// (pages_actions.go) issues the identical `SELECT 1 FROM pipelines ...
	// deleted_at IS NULL` first and answers 400. The 422 is reachable only for
	// a routine deleted between that check and the publishing transaction.
	// Pinned here so a change of answer is a decision, not a drift.
	if w.Code != 400 {
		t.Errorf("the refusal is now %d; it was 400 from resolveActionRoutines. If the 422 in checkPageCandidate "+
			"has become reachable, say so in the docs rather than leaving two codes for one condition", w.Code)
	}

	// And the review screen says the same thing in advance.
	_, after := reviewCall(t, h, ws, user, "OWNER", "health")
	if reviewBlockers(after)[reviewBlockerRoutineUnresolved] == "" {
		t.Errorf("the review snapshot offers no routine_unresolved blocker for a candidate whose routine is gone: %+v", after.Blockers)
	}
}

// TestPageProjectRollbackIsFencedOnTheSameLiveDefinition checks the claim
// pages_project_publish.go makes about `rollback_version`: that restoring a
// retained artifact is "fenced identically", because it replaces the same live
// declaration.
//
// Rollback skips the draft CAS (there is no draft in the request), so the live
// definition digest is the only thing standing between a reviewer's consent
// and a declaration they never saw. Both directions are asserted: unchanged,
// the restore lands; changed, it is refused and says which base moved.
func TestPageProjectRollbackIsFencedOnTheSameLiveDefinition(t *testing.T) {
	restorable := func(t *testing.T) (*PageHandler, string, string) {
		t.Helper()
		h, ws, user := reviewPublishedFixture(t)
		source := projectTestSource()
		source.Files[3].Content = "// the second publication\n"
		if w := projectPut(t, h, ws, user, "OWNER", "health", 1, source); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		build := reviewBuildRevision(t, h, ws, user, 2)
		one := int64(1)
		if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			BuildID: build, ExpectedRevision: 2, ExpectedPublication: &one, ReviewedCode: true,
		}); w.Code != 200 {
			t.Fatalf("second publication: %d %s", w.Code, w.Body.String())
		}
		return h, ws, user
	}

	t.Run("an unchanged live definition restores", func(t *testing.T) {
		h, ws, user := restorable(t)
		_, snapshot := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
		if snapshot.Candidate == nil {
			t.Fatalf("publication 1 is not offered as a candidate: %+v", snapshot.Blockers)
		}
		two := int64(2)
		if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			RollbackVersion: 1, ExpectedPublication: &two, ReviewedCode: true,
			ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
			ExpectedRoutineDigests:   reviewFenceFromSnapshot(t, snapshot),
		}); w.Code != 200 {
			t.Fatalf("restore publication 1: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("a live definition that moved after the review does not", func(t *testing.T) {
		h, ws, user := restorable(t)
		_, snapshot := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
		if snapshot.Candidate == nil {
			t.Fatalf("publication 1 is not offered as a candidate: %+v", snapshot.Blockers)
		}
		stored := reviewStoredSpec(t, h, "health")
		if _, err := h.db.Exec(`UPDATE pages SET spec_json=? WHERE slug='health'`, reviewRenamePanel(t, stored, "Renamed while the rollback was reviewed")); err != nil {
			t.Fatal(err)
		}
		two := int64(2)
		w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
			RollbackVersion: 1, ExpectedPublication: &two, ReviewedCode: true,
			ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
			ExpectedRoutineDigests:   reviewFenceFromSnapshot(t, snapshot),
		})
		if kind, _ := reviewConflictKind(t, w); w.Code != 409 || kind != "definition" {
			t.Fatalf("a rollback over a moved declaration returned %d %s (conflict %q), want 409 definition — "+
				"restoring old code against a declaration nobody reviewed binds it to panels and routines "+
				"nobody approved for it", w.Code, w.Body.String(), kind)
		}
	})
}
