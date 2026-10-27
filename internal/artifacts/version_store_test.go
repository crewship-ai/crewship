package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func versionFixture(t *testing.T, opts VersionOptions) (*VersionStore, VersionKey, VersionAuthorize, *database.DB) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`INSERT INTO users(id,email) VALUES('owner','owner@example.test')`, `INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','ws')`, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page','ws','page','Page','owner','{}')`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		var actor string
		err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE id='page' AND workspace_id='ws'`).Scan(&actor)
		return actor, err
	}
	opts.Enabled = true
	return NewVersionStore(db.DB, opts), VersionKey{"ws", "page"}, auth, db
}
func versionRequest(base int64, content string) VersionRequest {
	return VersionRequest{BaseVersion: base, EntryPoint: "index.html", Files: map[string]VersionFile{"index.html": {Content: []byte(content), MediaType: "text/html"}, "second.html": {Content: []byte("second"), MediaType: "text/html"}, "third.html": {Content: []byte("third"), MediaType: "text/html"}}}
}
func TestVersionStoreManifestCASRestoreShareAndRestart(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	v1, err := s.Apply(t.Context(), k, versionRequest(0, "first"), a)
	if err != nil || v1.Version != 1 {
		t.Fatalf("create %+v %v", v1, err)
	}
	if err = s.Share(t.Context(), k, 1, 1, a); err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(t.Context(), k, versionRequest(0, "stale"), a)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale %v", err)
	}
	v2, err := s.Apply(t.Context(), k, versionRequest(1, "changed"), a)
	if err != nil || v2.Version != 2 {
		t.Fatalf("publish %+v %v", v2, err)
	}
	v3, err := s.Restore(t.Context(), k, 2, 1, a)
	if err != nil || v3.Version != 3 {
		t.Fatalf("restore %+v %v", v3, err)
	}
	databasePath := db.Path()
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	s = NewVersionStore(reopened.DB, VersionOptions{Enabled: true})
	state, err := s.Read(t.Context(), k, a)
	if err != nil || state.LatestVersion != 3 || state.SharedVersion != 1 || string(state.Manifest.Files["index.html"].Content) != "first" {
		t.Fatalf("restart %+v %v", state, err)
	}
	versions, err := s.Versions(t.Context(), k, a)
	if err != nil || len(versions) != 3 {
		t.Fatalf("history %+v %v", versions, err)
	}
	old, err := s.ReadVersion(t.Context(), k, 2, a)
	if err != nil || string(old.Files["index.html"].Content) != "changed" {
		t.Fatalf("old %+v %v", old, err)
	}
}
func TestVersionStoreQuotaAtomicityAndLiveAuthority(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxHistoryBytes: 1300, WarningPercent: 10})
	_, err := s.Apply(t.Context(), k, versionRequest(0, "first"), a)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Read(t.Context(), k, a)
	if err != nil || !state.Quota.Warning || state.Quota.Retention != "keep-all" {
		t.Fatalf("quota %+v %v", state.Quota, err)
	}
	big := versionRequest(1, string(make([]byte, 1200)))
	_, err = s.Apply(t.Context(), k, big, a)
	if !errors.Is(err, ErrVersionQuota) {
		t.Fatalf("quota %v", err)
	}
	state, err = s.Read(t.Context(), k, a)
	if err != nil || state.LatestVersion != 1 {
		t.Fatalf("partial commit %+v %v", state, err)
	}
	denied := errors.New("revoked")
	revoke := func(context.Context, *sql.Tx) (string, error) { return "", denied }
	for _, fn := range []func() error{func() error { _, e := s.Apply(t.Context(), k, versionRequest(1, "edit"), revoke); return e }, func() error { _, e := s.Read(t.Context(), k, revoke); return e }, func() error { _, e := s.ReadVersion(t.Context(), k, 1, revoke); return e }, func() error { _, e := s.Versions(t.Context(), k, revoke); return e }, func() error { _, e := s.Restore(t.Context(), k, 1, 1, revoke); return e }, func() error { return s.Share(t.Context(), k, 1, 1, revoke) }} {
		if e := fn(); !errors.Is(e, denied) {
			t.Fatalf("authority %v", e)
		}
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM artifact_version_blobs`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("orphan blobs %d %v", count, err)
	}
}
func TestVersionStoreConcurrentPublishExactlyOneWinner(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "first"), a); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, content := range []string{"left", "right"} {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			_, e := s.Apply(t.Context(), k, versionRequest(1, c), a)
			results <- e
		}(content)
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("winners %d conflicts %d", ok, conflicts)
	}
}
func TestVersionStoreRejectsUnsafeAndIncompleteManifest(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	for _, name := range []string{"../escape.html", "/absolute.html", "x\\y.html", "x/../y.html", "a//b.html"} {
		r := versionRequest(0, "first")
		r.Files[name] = VersionFile{Content: []byte("bad")}
		if _, err := s.Apply(t.Context(), k, r, a); !errors.Is(err, ErrVersionInvalid) {
			t.Fatalf("path %q: %v", name, err)
		}
	}
	r := versionRequest(0, "first")
	r.EntryPoint = "missing.html"
	if _, err := s.Apply(t.Context(), k, r, a); !errors.Is(err, ErrVersionInvalid) {
		t.Fatalf("entrypoint: %v", err)
	}
	if _, err := s.Read(t.Context(), k, nil); err == nil {
		t.Fatal("missing authority accepted")
	}
}

func TestVersionStoreDisabledAndAtomicPageCreation(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{MaxHistoryBytes: 1300})
	called := false
	disabled := NewVersionStore(db.DB, VersionOptions{})
	if _, err := disabled.Apply(t.Context(), k, versionRequest(0, "first"), func(context.Context, *sql.Tx) (string, error) { called = true; return "owner", nil }); !errors.Is(err, ErrDisabled) || called {
		t.Fatalf("disabled authority called %t: %v", called, err)
	}
	createKey := VersionKey{"ws", "created"}
	create := func(ctx context.Context, tx *sql.Tx) (string, error) {
		if _, err := a(ctx, tx); err != nil {
			return "", err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('created','ws','created','Created','owner','{}')`)
		return "owner", err
	}
	tooLarge := versionRequest(0, string(make([]byte, 2000)))
	if _, err := s.Apply(t.Context(), createKey, tooLarge, create); !errors.Is(err, ErrVersionQuota) {
		t.Fatalf("create failure %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pages WHERE id='created'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("half-created page %d: %v", count, err)
	}
	if _, err := s.Apply(t.Context(), createKey, versionRequest(0, "first"), create); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(t.Context(), createKey, a); err != nil {
		t.Fatal(err)
	}
}

func TestVersionStoreAtomicViewHidesUnsharedHistory(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "shared"), a); err != nil {
		t.Fatal(err)
	}
	if err := s.Share(t.Context(), k, 1, 1, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(1, "private"), a); err != nil {
		t.Fatal(err)
	}
	reader := func(ctx context.Context, tx *sql.Tx) (string, VersionSelection, error) {
		actor, err := a(ctx, tx)
		return actor, VersionSelection{SharedOnly: true, IncludeHistory: true}, err
	}
	v, err := s.View(t.Context(), k, reader)
	if err != nil || v.State.LatestVersion != 1 || v.State.SharedVersion != 1 || string(v.State.Manifest.Files["index.html"].Content) != "shared" || len(v.Versions) != 1 || v.State.Quota.Retention != "shared-version-only" {
		t.Fatalf("shared view %+v %v", v, err)
	}
	invalidReader := func(ctx context.Context, tx *sql.Tx) (string, VersionSelection, error) {
		actor, err := a(ctx, tx)
		return actor, VersionSelection{Version: 2, SharedOnly: true}, err
	}
	if _, err = s.View(t.Context(), k, invalidReader); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("unshared version leak %v", err)
	}
	owner := func(ctx context.Context, tx *sql.Tx) (string, VersionSelection, error) {
		actor, err := a(ctx, tx)
		return actor, VersionSelection{IncludeHistory: true}, err
	}
	if v, err = s.View(t.Context(), k, owner); err != nil || len(v.Versions) != 2 || v.State.LatestVersion != 2 || string(v.State.Manifest.Files["index.html"].Content) != "private" {
		t.Fatalf("owner view %+v %v", v, err)
	}
}

func TestVersionStoreContentFamilyIsImmutableAndDoesNotConvertPages(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "HTML"), a); err != nil {
		t.Fatal(err)
	}
	native := NewVersionStore(db.DB, VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err := native.Read(t.Context(), k, a); !errors.Is(err, ErrVersionContentType) {
		t.Fatalf("native read adopted project: %v", err)
	}
	if _, err := native.Apply(t.Context(), k, versionRequest(1, "native"), a); !errors.Is(err, ErrVersionContentType) {
		t.Fatalf("native write converted project: %v", err)
	}
	if _, err := db.Exec(`UPDATE artifact_version_heads SET content_type='wordcraft' WHERE page_id='page'`); err == nil {
		t.Fatal("SQL changed immutable family")
	}
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('native','ws','native','Native','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	nk := VersionKey{"ws", "native"}
	// The native callback retains custody/lease validation in this transaction.
	if _, err := native.Apply(t.Context(), nk, versionRequest(0, "native original"), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(t.Context(), nk, a); !errors.Is(err, ErrVersionContentType) {
		t.Fatalf("project read adopted native history: %v", err)
	}
	if _, err := s.Apply(t.Context(), nk, versionRequest(1, "HTML"), a); !errors.Is(err, ErrVersionContentType) {
		t.Fatalf("project write converted native: %v", err)
	}
	if v, err := native.Read(t.Context(), nk, a); err != nil || string(v.Manifest.Files["index.html"].Content) != "native original" {
		t.Fatalf("native state changed: %+v %v", v, err)
	}
}
