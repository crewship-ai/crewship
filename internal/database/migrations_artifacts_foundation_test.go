package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func artifactsFoundationDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "foundation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestArtifactsFoundationFreshAndRepeatedMigration(t *testing.T) {
	db := artifactsFoundationDB(t)
	for _, table := range []string{"slides_writer_fence", "slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
		t.Run(table, func(t *testing.T) {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if table == "slides_writer_fence" {
				want = 1
			}
			if count != want {
				t.Fatalf("rows = %d, want %d", count, want)
			}
		})
	}
	for _, query := range []string{
		`SELECT generation FROM documents LIMIT 0`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Error(err)
			continue
		}
		rows.Close()
	}
	var seq int
	var boot string
	if err := db.QueryRow(`SELECT seq,boot_id FROM document_writer_fence WHERE id=1`).Scan(&seq, &boot); err != nil || seq != 0 || boot != "" {
		t.Errorf("initial document fence: seq=%d boot=%q err=%v", seq, boot, err)
	}
	var before, after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&after); err != nil || after != before {
		t.Fatalf("second migration changed ledger: before=%d after=%d err=%v", before, after, err)
	}
}

func TestArtifactsFoundationContentAndHumanOwnershipGuards(t *testing.T) {
	db := artifactsFoundationDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('foundation-owner','foundation-owner@example.test'),('foundation-successor','foundation-successor@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('foundation-ws','Foundation','foundation')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('foundation-crew','foundation-ws','Crew','foundation-crew')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	newPage := func(t *testing.T, id string, crewOwned bool) {
		t.Helper()
		owner, crew := any("foundation-owner"), any(nil)
		if crewOwned {
			owner, crew = nil, "foundation-crew"
		}
		if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,owner_crew_id,spec_json) VALUES(?,'foundation-ws',?,?,?,?,'{}')`, id, id, id, owner, crew); err != nil {
			t.Fatal(err)
		}
	}
	attach := func(kind, page string) error {
		var query string
		switch kind {
		case "document":
			query = `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('foundation-ws',?,1,X'01',X'5b5d')`
		case "slides":
			query = `INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES('foundation-ws',?,1,1,1,X'7b7d','fixture-hash')`
		case "draft":
			query = `INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_at) VALUES(?,printf('%064d',0),1,'{}','2026-10-16T00:00:00Z')`
		case "panel":
			query = `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES('panel-'||?,?,'fixture','metric.v1','foundation-crew','script','fixture',60)`
			_, err := db.Exec(query, page, page)
			return err
		default:
			panic("unknown fixture content kind")
		}
		_, err := db.Exec(query, page)
		return err
	}
	for _, pair := range [][2]string{
		{"document", "slides"}, {"slides", "document"},
		{"slides", "draft"}, {"draft", "slides"},
		{"slides", "panel"}, {"panel", "slides"},
		{"document", "draft"}, {"draft", "document"},
		{"document", "panel"}, {"panel", "document"},
	} {
		t.Run(pair[0]+"-then-"+pair[1], func(t *testing.T) {
			page := pair[0] + "-" + pair[1]
			newPage(t, page, false)
			if err := attach(pair[0], page); err != nil {
				t.Fatalf("first content: %v", err)
			}
			err := attach(pair[1], page)
			guard := ""
			if err != nil {
				guard = strings.ToLower(err.Error())
			}
			if !strings.Contains(guard, "another content type") && !strings.Contains(guard, "require an empty page") && !strings.Contains(guard, "already contains slides") {
				t.Fatalf("second content must fail with content-type guard: %v", err)
			}
		})
	}
	for _, kind := range []string{"document", "slides"} {
		t.Run(kind+"-human-owner", func(t *testing.T) {
			newPage(t, kind+"-crew", true)
			if err := attach(kind, kind+"-crew"); err == nil || !strings.Contains(err.Error(), "human") {
				t.Fatalf("crew-owned Page must reject native content: %v", err)
			}
			page := kind + "-human"
			newPage(t, page, false)
			if err := attach(kind, page); err != nil {
				t.Fatal(err)
			}
			if kind == "document" {
				var generation int
				if err := db.QueryRow(`SELECT generation FROM documents WHERE workspace_id='foundation-ws' AND page_id=?`, page).Scan(&generation); err != nil || generation != 0 {
					t.Fatalf("initial document generation=%d err=%v", generation, err)
				}
			}
			if _, err := db.Exec(`UPDATE pages SET owner_user_id='foundation-successor' WHERE id=?`, page); err != nil {
				t.Fatalf("explicit human succession: %v", err)
			}
			if _, err := db.Exec(`UPDATE pages SET owner_user_id=NULL,owner_crew_id='foundation-crew' WHERE id=?`, page); err == nil || !strings.Contains(err.Error(), "human") {
				t.Fatalf("native Page must retain human ownership: %v", err)
			}
		})
	}
}
