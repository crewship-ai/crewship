package documents

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

var ctx = context.Background()
var key = Key{"workspace-a", "page-a"}
var projection = []byte(`[{"id":"one","hash":"h1"}]`)

func allow(context.Context, *sql.Tx) (string, error) { return "server-actor", nil }
func openTest(t *testing.T, path string) *database.DB {
	t.Helper()
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func fixture(t *testing.T, o Options) (*Store, *database.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "documents.db")
	db := openTest(t, path)
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('document-owner','document-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('workspace-a','A','document-a'),('workspace-b','B','document-b')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page-a','workspace-a','a','A','document-owner','{}'),('page-b','workspace-b','b','B','document-owner','{}'),('page-c','workspace-a','c','C','document-owner','{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := New(db.DB, o)
	if _, err := s.Init(ctx, key, 1, []byte("baseline"), projection, allow); err != nil {
		t.Fatal(err)
	}
	return s, db, path
}

func TestInitRequestRetriesRequireExactPayloadAndActor(t *testing.T) {
	tests := []struct {
		name        string
		snapshot    []byte
		projection  []byte
		fingerprint string
		schema      int
		actor       string
		want        error
	}{
		{"exact retry", []byte("initial"), projection, "original", 1, "server-actor", nil},
		{"changed snapshot", []byte("changed"), projection, "original", 1, "server-actor", ErrDuplicate},
		{"changed projection", []byte("initial"), []byte(`[]`), "original", 1, "server-actor", ErrDuplicate},
		{"changed fingerprint", []byte("initial"), projection, "changed", 1, "server-actor", ErrDuplicate},
		{"changed schema", []byte("initial"), projection, "original", 2, "server-actor", ErrDuplicate},
		{"changed actor", []byte("initial"), projection, "original", 1, "other-actor", ErrDuplicate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{})
			target := Key{"workspace-a", "page-c"}
			if _, err := s.InitRequest(ctx, target, "init-request", "original", 1, []byte("initial"), projection, allow); err != nil {
				t.Fatal(err)
			}
			authorize := func(context.Context, *sql.Tx) (string, error) { return tt.actor, nil }
			got, err := s.InitRequest(ctx, target, "init-request", tt.fingerprint, tt.schema, tt.snapshot, tt.projection, authorize)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if err == nil && (got.HeadRev != 0 || string(got.Snapshot) != "initial") {
				t.Fatalf("retry state: %#v", got)
			}
			var n int
			if err := db.QueryRow("SELECT count(*) FROM document_receipts WHERE page_id='page-c'").Scan(&n); err != nil || n != 1 {
				t.Fatalf("receipt count %d: %v", n, err)
			}
		})
	}
}

func TestInitValidationRollsBack(t *testing.T) {
	tests := []struct {
		name                 string
		schema               int
		snapshot, projection []byte
		opts                 Options
		want                 error
	}{
		{"unsupported schema", LatestSchema + 1, []byte("state"), projection, Options{}, ErrSchema},
		{"empty snapshot", 1, nil, projection, Options{}, nil},
		{"invalid projection", 1, []byte("state"), []byte(`{`), Options{}, nil},
		{"document quota", 1, []byte("state"), projection, Options{MaxDocuments: 1}, ErrQuota},
		{"byte quota", 1, make([]byte, 64), projection, Options{MaxDocumentBytes: 64}, ErrQuota},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, db, _ := fixture(t, tt.opts)
			_, err := s.InitRequest(ctx, Key{"workspace-a", "page-c"}, "invalid", "fp", tt.schema, tt.snapshot, tt.projection, allow)
			if err == nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			for _, table := range []string{"documents", "document_receipts"} {
				var n int
				if err := db.QueryRow("SELECT count(*) FROM " + table + " WHERE page_id='page-c'").Scan(&n); err != nil || n != 0 {
					t.Fatalf("%s count %d: %v", table, n, err)
				}
			}
		})
	}
}

func TestInitAndReadRecheckAuthority(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	revoked := errors.New("revoked")
	for _, tt := range []struct {
		name      string
		authorize Authorize
	}{
		{"revoked", func(context.Context, *sql.Tx) (string, error) { return "", revoked }},
		{"empty actor", func(context.Context, *sql.Tx) (string, error) { return "", nil }},
		{"missing authority", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Read(ctx, key, tt.authorize); err == nil {
				t.Fatal("unauthorized read succeeded")
			}
			if _, err := s.Init(ctx, key, 1, []byte("baseline"), projection, tt.authorize); err == nil {
				t.Fatal("unauthorized receipt replay succeeded")
			}
		})
	}
}

func TestReadDurableStateAcrossRestart(t *testing.T) {
	_, db, path := fixture(t, Options{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, path)
	defer reopened.Close()
	s := New(reopened.DB, Options{})
	got, err := s.Read(ctx, key, allow)
	if err != nil || got.Schema != 1 || got.HeadRev != 0 || got.SnapshotRev != 0 || string(got.Snapshot) != "baseline" || string(got.Projection) != string(projection) || len(got.Updates) != 0 {
		t.Fatalf("restart state %#v: %v", got, err)
	}
	if _, err := s.Read(ctx, Key{"workspace-b", "page-a"}, allow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace read: %v", err)
	}
}

func TestReadRejectsCorruptReplayCoverage(t *testing.T) {
	for _, tt := range []struct {
		name      string
		head      int
		revisions []int
		want      string
	}{
		{"ordered replay", 2, []int{2, 1}, ""},
		{"missing first update", 2, []int{2}, "not contiguous"},
		{"missing last update", 2, []int{1}, "does not cover"},
		{"updates beyond head", 1, []int{1, 2}, "does not cover"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{})
			if _, err := db.Exec("UPDATE documents SET head_rev=? WHERE page_id='page-a'", tt.head); err != nil {
				t.Fatal(err)
			}
			for _, rev := range tt.revisions {
				if _, err := db.Exec("INSERT INTO document_updates(workspace_id,page_id,rev,payload) VALUES(?,?,?,?)", key.WorkspaceID, key.PageID, rev, []byte("update")); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Read(ctx, key, allow)
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("got %v, want %s", err, tt.want)
				}
				return
			}
			if err != nil || len(got.Updates) != 2 || got.Updates[0].Rev != 1 || got.Updates[1].Rev != 2 {
				t.Fatalf("ordered replay %#v: %v", got, err)
			}
		})
	}
}

func TestDocumentScopeAndPageDeletion(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	if _, err := s.Init(ctx, Key{"workspace-b", "page-c"}, 1, []byte("other"), projection, allow); err == nil {
		t.Fatal("cross-workspace document initialized")
	}
	if _, err := db.Exec("DELETE FROM pages WHERE id='page-a'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, key, allow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted page read: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM document_receipts WHERE page_id='page-a'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("receipts after cascade %d: %v", n, err)
	}
}

func TestDocumentTypeAttachmentsAreMutuallyExclusive(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	if _, e := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('document-panel-crew','workspace-a','Panel producer','document-panel-crew')`); e != nil {
		t.Fatal(e)
	}
	attachments := []struct{ name, query string }{
		{"project", `INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_at) VALUES(?,printf('%064d',0),1,'{}','2026-10-15T00:00:00Z')`},
		{"panel", `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES('document-collision-panel',?,'panel','metric.v1','document-panel-crew','script','script',60)`},
	}
	for _, attachment := range attachments {
		t.Run(attachment.name, func(t *testing.T) {
			if _, e := db.Exec(attachment.query, key.PageID); e == nil {
				t.Fatal("attached another content type to a document")
			}
			state, e := s.Read(ctx, key, allow)
			if e != nil || state.HeadRev != 0 || string(state.Snapshot) != "baseline" || len(state.Updates) != 0 {
				t.Fatalf("rejected type attachment modified document: %#v %v", state, e)
			}
			// Ordinary human-owned pages still accept their existing attachment type.
			pageID := "existing-" + attachment.name
			if _, e := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,'workspace-a',?,?,'document-owner','{}')`, pageID, pageID, pageID); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Exec(attachment.query, pageID); e != nil {
				t.Fatalf("ordinary page attachment rejected: %v", e)
			}
			if _, e := s.InitRequest(ctx, Key{key.WorkspaceID, pageID}, "init-collision", "init-collision", 1, []byte("new document"), projection, allow); e == nil {
				t.Fatal("initialized Docs over another existing content type")
			}
			var count int
			if e := db.QueryRow(`SELECT count(*) FROM documents WHERE page_id=?`, pageID).Scan(&count); e != nil || count != 0 {
				t.Fatalf("rejected initialization persisted document: %d %v", count, e)
			}
		})
	}
}

func TestDocumentsRequirePersistentHumanOwner(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	if _, e := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('document-owning-crew','workspace-a','Crew owner','document-owning-crew')`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES('crew-page','workspace-a','crew-page','Crew page','document-owning-crew','{}')`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Init(ctx, Key{"workspace-a", "crew-page"}, 1, []byte("crew document"), projection, allow); e == nil {
		t.Fatal("initialized a crew-owned document")
	}
	if _, e := db.Exec(`UPDATE pages SET owner_user_id=NULL,owner_crew_id='document-owning-crew' WHERE id=?`, key.PageID); e == nil {
		t.Fatal("implicitly transferred document ownership to crew")
	}
	if _, e := db.Exec(`UPDATE pages SET owner_user_id=NULL WHERE id=?`, key.PageID); e == nil {
		t.Fatal("removed persistent document owner")
	}
	if _, e := db.Exec(`INSERT INTO users(id,email) VALUES('document-successor','document-successor@example.test')`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`UPDATE pages SET owner_user_id='document-successor' WHERE id=?`, key.PageID); e != nil {
		t.Fatalf("explicit human succession rejected: %v", e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || string(v.Snapshot) != "baseline" || v.HeadRev != 0 {
		t.Fatalf("ownership succession changed document: %#v %v", v, e)
	}
	if _, e := db.Exec(`DELETE FROM pages WHERE id=?`, key.PageID); e != nil {
		t.Fatalf("explicit page deletion blocked: %v", e)
	}
	if _, e := s.Read(ctx, key, allow); !errors.Is(e, ErrNotFound) {
		t.Fatalf("document survived explicit page deletion: %v", e)
	}
}
