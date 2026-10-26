package database

import (
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

func crewGrantSchema(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	rows, err := db.Query(`SELECT type||':'||name,sql FROM sqlite_schema WHERE type IN ('view','trigger','index') AND sql IS NOT NULL AND name<>'live_access_grants'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, ddl string
		if err = rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		result[name] = ddl
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCrewGrantUpgradePreservesAuthorityAndRollsBackDDL(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "populated upgrade", true: "copy target collision"}[fail], func(t *testing.T) {
			db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := migrateBeforeCrewGrantFoundation(t, db, logger); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`INSERT INTO users(id,email) VALUES('o','upgrade-owner@test.invalid'),('h','upgrade-guest@test.invalid')`,
				`INSERT INTO workspaces(id,name,slug) VALUES('w','Upgrade','upgrade')`,
				`INSERT INTO workspace_members(id,user_id,workspace_id,role,access_mode) VALUES('o','o','w','OWNER','trusted'),('h','h','w','MEMBER','restricted')`,
				`INSERT INTO crews(id,workspace_id,name,slug) VALUES('c','w','Crew','crew')`,
				`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('a','w','c','A','a','AGENT')`,
				`INSERT INTO projects(id,workspace_id,name,slug) VALUES('p','w','Project','p')`,
				`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('r','w','Routine','r','{}','hash','active')`,
				`INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES('ga','h','agent','a','run','o','original')`,
				`INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('gp','h','project','p','read','o','original')`,
				`INSERT INTO access_grants(id,member_id,resource_kind,routine_id,operation,created_by,created_at) VALUES('gr','h','routine','r','view','o','original')`,
				`CREATE VIEW upgrade_projection AS SELECT id FROM live_access_grants`,
				`CREATE VIEW upgrade_transitive AS SELECT id FROM upgrade_projection`,
				`CREATE TRIGGER upgrade_probe AFTER UPDATE OF name ON workspaces BEGIN SELECT count(*) FROM upgrade_transitive; END`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			before := crewGrantSchema(t, db)
			var revision int64
			if err := db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='h'`).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err := db.Exec(`CREATE TABLE access_grants_next(keep TEXT)`); err != nil {
					t.Fatal(err)
				}
			}
			err := Migrate(t.Context(), db, logger)
			if (err != nil) != fail {
				t.Fatalf("migration=%v failure=%v", err, fail)
			}
			after := crewGrantSchema(t, db)
			for name, ddl := range before {
				if after[name] != ddl {
					t.Fatalf("schema object lost/changed: %s", name)
				}
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM access_grants WHERE created_by='o' AND created_at='original'`).Scan(&count); err != nil || count != 3 {
				t.Fatalf("old grants: %d %v", count, err)
			}
			var current int64
			if err := db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='h'`).Scan(&current); err != nil || current != revision {
				t.Fatalf("old revision %d -> %d: %v", revision, current, err)
			}
			for _, pragma := range []string{"foreign_keys", "legacy_alter_table"} {
				var value int
				if err := db.QueryRow("PRAGMA " + pragma).Scan(&value); err != nil {
					t.Fatal(err)
				}
				want := 0
				if pragma == "foreign_keys" {
					want = 1
				}
				if value != want {
					t.Fatalf("leaked pragma %s=%d", pragma, value)
				}
			}
			if _, err := db.Exec(`UPDATE workspaces SET name='Still valid' WHERE id='w'`); err != nil {
				t.Fatal(err)
			}
			if !fail {
				if err := Migrate(t.Context(), db, logger); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, crewGrantSchema(t, db)) {
					t.Fatal("repeat upgrade changed schema")
				}
				if _, err := db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE id='o'`); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM live_access_grants`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("inert issuer still grants authority: %d %v", count, err)
				}
			} else {
				if err := db.QueryRow(`SELECT count(*) FROM _migrations WHERE name='crew_resource_grants'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed upgrade recorded: %d %v", count, err)
				}
			}
		})
	}
}

func TestCrewGrantUpgradeRefusesCaseInsensitiveIncomingForeignKeys(t *testing.T) {
	for _, parent := range []string{"ACCESS_GRANTS", "AcCeSs_GrAnTs"} {
		t.Run(parent, func(t *testing.T) {
			db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := migrateBeforeCrewGrantFoundation(t, db, logger); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`INSERT INTO users(id,email) VALUES('o','fk-owner@test.invalid'),('h','fk-guest@test.invalid')`,
				`INSERT INTO workspaces(id,name,slug) VALUES('w','FK','fk')`,
				`INSERT INTO workspace_members(id,user_id,workspace_id,role,access_mode) VALUES('o','o','w','OWNER','trusted'),('h','h','w','MEMBER','restricted')`,
				`INSERT INTO crews(id,workspace_id,name,slug) VALUES('c','w','Crew','crew')`,
				`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('a','w','c','Agent','agent','AGENT')`,
				`INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES('g','h','agent','a','run','o','preserved')`,
				`CREATE TABLE grant_extension(id TEXT PRIMARY KEY, grant_id TEXT REFERENCES ` + parent + `(id) ON DELETE CASCADE)`,
				`INSERT INTO grant_extension VALUES('child','g')`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			before := crewGrantSchema(t, db)
			var beforeRevision int64
			if err := db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='h'`).Scan(&beforeRevision); err != nil {
				t.Fatal(err)
			}
			err := Migrate(t.Context(), db, logger)
			if err == nil {
				t.Fatal("migration accepted an incoming foreign key")
			}
			if !reflect.DeepEqual(before, crewGrantSchema(t, db)) {
				t.Fatal("refused migration changed dependent schema")
			}
			for _, query := range []string{`SELECT count(*) FROM grant_extension WHERE id='child' AND grant_id='g'`, `SELECT count(*) FROM access_grants WHERE id='g' AND created_at='preserved'`} {
				var n int
				if err = db.QueryRow(query).Scan(&n); err != nil || n != 1 {
					t.Fatalf("lost populated foreign-key data: count=%d error=%v", n, err)
				}
			}
			var afterRevision int64
			if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='h'`).Scan(&afterRevision); err != nil || afterRevision != beforeRevision {
				t.Fatal("refused migration changed revision", beforeRevision, afterRevision, err)
			}
			var applied int
			if err = db.QueryRow(`SELECT count(*) FROM _migrations WHERE name='crew_resource_grants'`).Scan(&applied); err != nil || applied != 0 {
				t.Fatal("refused migration recorded", applied, err)
			}
		})
	}
}

// Build the actual pre-foundation registry, excluding later dependent upgrades.
// This keeps the populated upgrade test honest as new crew migrations append.
func migrateBeforeCrewGrantFoundation(t *testing.T, db *sql.DB, logger *slog.Logger) error {
	t.Helper()
	if err := ensureMigrationsTable(t.Context(), db); err != nil {
		return err
	}
	var before []migration
	for _, m := range migrations {
		if m.version < 20261025233100 {
			before = append(before, m)
		}
	}
	return applyRegistry(t.Context(), db, before, logger)
}
