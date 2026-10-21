package database

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveGrantIssuerUpgradePreservesValidShares(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "issuer-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	version := 0
	for _, m := range migrations {
		if m.name == "live_grant_issuers" {
			version = m.version
		}
	}
	if version == 0 {
		t.Fatal("issuer migration not registered")
	}
	if err = applyMigrationsUpTo(t.Context(), db, version-1, logger); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`PRAGMA foreign_keys=ON`,
		`INSERT INTO users(id,email) VALUES('issuer','issuer@example.test'),('former','former@example.test'),('guest','guest@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('w','Workspace','issuer-upgrade')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('owner','w','issuer','OWNER','trusted'),('former-admin','w','former','MEMBER','trusted'),('recipient','w','guest','VIEWER','restricted')`,
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES('p','w','Project','issuer-project')`,
		`INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('valid','recipient','project','p','read','issuer','2026-10-13T00:00:00Z'),('invalid','recipient','project','p','list','former','2026-10-13T00:00:00Z')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var before, after int64
	if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='recipient'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM live_access_grants WHERE id='valid' AND created_by='issuer' AND created_at='2026-10-13T00:00:00Z'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("valid grant metadata changed: count=%d err=%v", count, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM live_access_grants WHERE id='invalid'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("inert old share remains usable: count=%d err=%v", count, err)
	}
	if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='recipient'`).Scan(&after); err != nil || after <= before {
		t.Fatalf("upgrade did not fence revoked authority: before=%d after=%d err=%v", before, after, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM access_grants WHERE id='invalid' AND created_by='former' AND created_at='2026-10-13T00:00:00Z'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("inert grant metadata lost: count=%d err=%v", count, err)
	}
	if _, err = db.Exec(`UPDATE workspace_members SET role='ADMIN' WHERE id='former-admin'`); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM live_access_grants`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("restored issuer did not reactivate preserved share: count=%d err=%v", count, err)
	}
}
