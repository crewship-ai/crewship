package backup_test

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

// PR1 storage-shape fixture only: manually seeded receipt rows are NOT capture,
// issuance, bootstrap or runtime authority evidence. Genuine capture stays PR2.
func syntheticReceiptExportFixture(t *testing.T, tier any) (*sql.DB, string) {
	return syntheticReceiptExportFixtureKind(t, tier, "")
}
func syntheticReceiptExportFixtureKind(t *testing.T, tier any, kind string) (*sql.DB, string) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "export.db"), RawQuery: "_pragma=foreign_keys(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = database.Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	if err = database.SeedBundledSkills(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	workspace := seedExecutionOriginHistory(t, db)
	raw := `{"name":"Supported metadata – ž","steps":[]}`
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	var origin any = "execution-root"
	result, err := db.Exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version,author_crew_id,author_agent_id,chat_origin_id,execution_tier_json) SELECT 'export-p',?,'export-p','Export',?,?,0,crew_id,id,'execution-root',? FROM agents WHERE id='a_bob'`, workspace, raw, hash, tier)
	if err != nil {
		t.Fatal(err)
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		t.Fatalf("source insert: %d %v", n, e)
	}
	if kind == "invalid-utf8-tier" {
		tier = string([]byte{0xff})
		if _, err = db.Exec(`UPDATE pipelines SET execution_tier_json=CAST(X'ff' AS TEXT) WHERE id='export-p'`); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "invalid-utf8-raw" {
		raw = string([]byte{0xff})
		hash = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
		if _, err = db.Exec(`UPDATE pipelines SET definition_json=CAST(X'ff' AS TEXT),definition_hash=? WHERE id='export-p'`, hash); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "blob-origin" {
		origin = []byte("binary-origin")
		if _, err = db.Exec(`INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES(CAST('binary-origin' AS BLOB),?,'binary-root','fixture-chat','fixture-message','{}','2026-10-18T00:00:00Z')`, workspace); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`UPDATE pipelines SET chat_origin_id=CAST('binary-origin' AS BLOB) WHERE id='export-p'`); err != nil {
			t.Fatal(err)
		}
	}
	if hash != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) {
		t.Fatal("SETUP expected exact raw SHA mismatch")
	}
	nativeKind := func(v any) string {
		if v == nil {
			return "null"
		}
		switch v.(type) {
		case string:
			return "text"
		case []byte:
			return "blob"
		}
		t.Fatalf("SETUP unsupported native expectation: %T", v)
		return ""
	}
	expected := []any{"export-p", int64(0), raw, hash, "c_alpha", "a_bob", origin, tier, "text", "text", nativeKind(origin), nativeKind(tier)}
	if actual := syntheticReceiptExportNativeFixtureTuple(t, db, false); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("SETUP source native precondition: got %#v want %#v", actual, expected)
	}
	// Explicit synthetic storage row; PR1 registers no Go capture cutover.
	if _, err = db.Exec(`INSERT INTO main.pipeline_legacy_head_receipts(id,pipeline_id,head_version,definition_json,definition_hash,author_crew_id,author_agent_id,chat_origin_id,execution_tier_json) SELECT 'synthetic-export-receipt',id,head_version,definition_json,definition_hash,author_crew_id,author_agent_id,chat_origin_id,execution_tier_json FROM main.pipelines WHERE id='export-p'`); err != nil {
		t.Fatal(err)
	}
	var fk, broken int
	if err = db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("FK prerequisite: %d %v", fk, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
		t.Fatalf("FK graph: %d %v", broken, err)
	}
	var receiptCount int
	var receiptID string
	if err = db.QueryRow(`SELECT COUNT(*),MIN(id) FROM pipeline_legacy_head_receipts WHERE pipeline_id='export-p'`).Scan(&receiptCount, &receiptID); err != nil || receiptCount != 1 || receiptID == "" {
		t.Fatalf("SETUP synthetic receipt presence: %d %q %v", receiptCount, receiptID, err)
	}
	for _, receipt := range []bool{false, true} {
		if actual := syntheticReceiptExportNativeFixtureTuple(t, db, receipt); !reflect.DeepEqual(actual, expected) {
			t.Fatalf("SETUP synthetic native storage receipt=%v: got %#v want %#v", receipt, actual, expected)
		}
	}
	return db, workspace
}

func syntheticReceiptExportNativeFixtureTuple(t *testing.T, db *sql.DB, receipt bool) []any {
	t.Helper()
	query := `SELECT id,head_version,definition_json,definition_hash,author_crew_id,author_agent_id,chat_origin_id,execution_tier_json,typeof(definition_json),typeof(definition_hash),typeof(chat_origin_id),typeof(execution_tier_json) FROM pipelines WHERE id='export-p'`
	if receipt {
		query = `SELECT pipeline_id,head_version,definition_json,definition_hash,author_crew_id,author_agent_id,chat_origin_id,execution_tier_json,typeof(definition_json),typeof(definition_hash),typeof(chat_origin_id),typeof(execution_tier_json) FROM pipeline_legacy_head_receipts WHERE pipeline_id='export-p'`
	}
	values := make([]any, 12)
	ptr := make([]any, len(values))
	for i := range values {
		ptr[i] = &values[i]
	}
	if err := db.QueryRow(query).Scan(ptr...); err != nil {
		t.Fatalf("SETUP native tuple receipt=%v: %v", receipt, err)
	}
	// Driver allocation detail only: preserve SQL NULL (nil interface), BLOB kind
	// and bytes; typeof columns remain unchanged and mandatory in comparisons.
	for i, value := range values {
		if blob, ok := value.([]byte); ok && blob == nil {
			values[i] = []byte{}
		}
	}
	return values
}

func syntheticReceiptExportState(t *testing.T, db *sql.DB) []any {
	t.Helper()
	v := make([]any, 15)
	ptr := make([]any, len(v))
	for i := range v {
		ptr[i] = &v[i]
	}
	err := db.QueryRow(`SELECT p.id,p.definition_json,p.definition_hash,p.head_version,p.author_crew_id,p.author_agent_id,p.chat_origin_id,p.execution_tier_json,r.id,r.definition_json,r.definition_hash,r.chat_origin_id,r.execution_tier_json,r.created_at,(SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id=p.id) FROM pipelines p JOIN pipeline_legacy_head_receipts r ON r.pipeline_id=p.id WHERE p.id='export-p'`).Scan(ptr...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSyntheticReceiptExportSupportedNullAndText(t *testing.T) {
	for _, tier := range []any{nil, "", "{}"} {
		t.Run(fmt.Sprintf("%T-%v", tier, tier), func(t *testing.T) {
			source, ws := syntheticReceiptExportFixture(t, tier)
			before := syntheticReceiptExportState(t, source)
			dump, err := backup.DumpWorkspace(t.Context(), source, ws)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise real JSON as well as original ordinary/fork ID remapping.
			encoded, err := backup.MarshalDump(dump)
			if err != nil {
				t.Fatal(err)
			}
			for _, fork := range []bool{false, true} {
				copyDump, err := backup.UnmarshalDump(encoded)
				if err != nil {
					t.Fatal(err)
				}
				target := openMigratedDB(t)
				if fork {
					target = source
					if err = backup.RemapIDs(t.Context(), source, copyDump); err != nil {
						t.Fatal(err)
					}
					copyDump.Tables["workspaces"][0]["slug"] = "supported-export-fork"
				}
				if err = backup.RestoreDump(t.Context(), target, copyDump); err != nil {
					t.Fatal(err)
				}
				id := copyDump.Tables["pipeline_legacy_head_receipts"][0]["id"]
				var rt, pt any
				var broken int
				if err = target.QueryRow(`SELECT r.execution_tier_json,p.execution_tier_json FROM pipeline_legacy_head_receipts r JOIN pipelines p ON p.id=r.pipeline_id WHERE r.id=?`, id).Scan(&rt, &pt); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(rt, tier) || !reflect.DeepEqual(pt, tier) {
					t.Fatalf("supported native values changed: %#v %#v want %#v", rt, pt, tier)
				}
				if err = target.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
					t.Fatalf("restored FK: %d %v", broken, err)
				}
			}
			if !reflect.DeepEqual(before, syntheticReceiptExportState(t, source)) {
				t.Fatal("export/fork changed original bindings")
			}
		})
	}
}

func TestSyntheticReceiptExportUnsupportedNativeBindings(t *testing.T) {
	for _, kind := range []string{"blob-tier", "empty-blob-tier", "invalid-utf8-tier", "invalid-utf8-raw", "blob-origin"} {
		t.Run(kind, func(t *testing.T) {
			var tier any = "{}"
			if kind == "blob-tier" {
				tier = []byte("{}")
			}
			if kind == "empty-blob-tier" {
				tier = make([]byte, 0)
			}
			source, ws := syntheticReceiptExportFixtureKind(t, tier, kind)
			var broken int
			if err := source.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
				t.Fatalf("native fixture FK: %d %v", broken, err)
			}
			before := syntheticReceiptExportState(t, source)
			dump, err := backup.DumpWorkspace(t.Context(), source, ws)
			if !errors.Is(err, backup.ErrLegacyHeadReceiptExportUnsupported) || dump != nil {
				t.Fatalf("unsupported export succeeded: dump%v err%v", dump != nil, err)
			}
			if !reflect.DeepEqual(before, syntheticReceiptExportState(t, source)) {
				t.Fatal("denial changed source binding/proof")
			}
		})
	}
}

func TestSyntheticReceiptExportWorkspaceIsolation(t *testing.T) {
	source, _ := syntheticReceiptExportFixture(t, []byte("{}"))
	if _, err := source.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('healthy-export-w','Healthy','healthy-export-w')`); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, "healthy-export-w")
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["pipeline_legacy_head_receipts"]) != 0 {
		t.Fatal("foreign receipt exported")
	}
}

func TestSyntheticReceiptExportPartialAndUnknownSchema(t *testing.T) {
	for _, kind := range []string{"missing-column", "extra-prefix", "view", "missing-table-modern", "generated-column"} {
		t.Run(kind, func(t *testing.T) {
			source, ws := syntheticReceiptExportFixture(t, nil)
			statement := `ALTER TABLE pipeline_legacy_head_receipts DROP COLUMN execution_tier_json`
			if kind == "generated-column" {
				statement = `ALTER TABLE pipeline_legacy_head_receipts ADD COLUMN derived_guard TEXT GENERATED ALWAYS AS (definition_hash) VIRTUAL`
			}
			if kind == "missing-table-modern" {
				statement = `DROP TABLE pipeline_legacy_head_receipts`
			}
			if kind == "extra-prefix" {
				statement = `CREATE TABLE pipeline_legacy_head_receipt_future(id TEXT)`
			}
			if kind == "view" {
				statement = `ALTER TABLE pipeline_legacy_head_receipts RENAME TO pipeline_legacy_head_receipts_old; CREATE VIEW pipeline_legacy_head_receipts AS SELECT * FROM pipeline_legacy_head_receipts_old`
			}
			if _, err := source.Exec(statement); err != nil {
				t.Fatalf("schema fixture: %v", err)
			}
			dump, err := backup.DumpWorkspace(t.Context(), source, ws)
			if !errors.Is(err, backup.ErrLegacyHeadReceiptExportUnsupported) || dump != nil {
				t.Fatalf("partial/unknown accepted: %v", err)
			}
		})
	}
}

func TestSyntheticReceiptCreateBackupCategoryBoundary(t *testing.T) {
	for _, include := range []bool{true, false} {
		t.Run(fmt.Sprint(include), func(t *testing.T) {
			source, ws := syntheticReceiptExportFixture(t, []byte("{}"))
			// CreateBackup's existing target resolver counts agents while crew rows
			// remain open. Only this real-backup fixture needs a second connection;
			// TEMP metadata fixtures retain their default single connection.
			source.SetMaxOpenConns(2)
			var connections []*sql.Conn
			t.Cleanup(func() {
				for _, conn := range connections {
					_ = conn.Close()
				}
			})
			for i := 0; i < 2; i++ {
				conn, err := source.Conn(t.Context())
				if err != nil {
					t.Fatalf("SETUP category fixture connection %d: %v", i, err)
				}
				connections = append(connections, conn)
			}
			for i, conn := range connections {
				var enabled int
				if err := conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
					t.Fatalf("SETUP category FK connection %d: %d %v", i, enabled, err)
				}
			}
			var closeErr error
			for _, conn := range connections {
				closeErr = errors.Join(closeErr, conn.Close())
			}
			connections = nil
			if closeErr != nil {
				t.Fatalf("SETUP category connection release: %v", closeErr)
			}
			before := syntheticReceiptExportState(t, source)
			dir := t.TempDir()
			var phases []string
			categories := []string{backup.CategoryRoutines, backup.CategoryAgents}
			if !include {
				categories = []string{backup.CategoryMemory, backup.CategoryAgents}
			}
			result, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: dir, Actor: backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}, Passphrase: "synthetic-export-boundary-passphrase", Categories: categories, Progress: func(phase string) { phases = append(phases, phase) }})
			if include {
				if !errors.Is(err, backup.ErrLegacyHeadReceiptExportUnsupported) || result != nil {
					t.Fatalf("selected unsupported export: %v", err)
				}
				for _, phase := range phases {
					if phase == "pack" || phase == "encrypt" || phase == "write" {
						t.Fatalf("failed metadata export reached finalization: %v", phases)
					}
				}
				matches, e := filepath.Glob(filepath.Join(dir, "*.tar.zst"))
				if e != nil || len(matches) != 0 {
					t.Fatalf("published failed bundle: %v %v", matches, e)
				}
			} else {
				if err != nil || result == nil {
					t.Fatalf("excluded routines blocked: %v", err)
				}
				if _, ok := result.Manifest.Contents.TableRowCounts["pipeline_legacy_head_receipts"]; ok {
					t.Fatal("excluded receipt counted")
				}
				if _, e := os.Stat(result.Path); e != nil {
					t.Fatal(e)
				}
			}
			if !reflect.DeepEqual(before, syntheticReceiptExportState(t, source)) {
				t.Fatal("CreateBackup changed protected source")
			}
		})
	}
}

func TestSyntheticReceiptExportExactNamespaceFence(t *testing.T) {
	for _, shadow := range []string{"pipelines", "pipeline_legacy_head_receipts", "unrelated_temp"} {
		t.Run(shadow, func(t *testing.T) {
			source, ws := syntheticReceiptExportFixture(t, nil)
			statement := `CREATE TEMP TABLE unrelated_temp(id TEXT)`
			if shadow != "unrelated_temp" {
				statement = `CREATE TEMP TABLE ` + shadow + ` AS SELECT * FROM main.` + shadow + ` WHERE 0`
			}
			if _, err := source.Exec(statement); err != nil {
				t.Fatal(err)
			}
			dump, err := backup.DumpWorkspace(t.Context(), source, ws)
			if shadow == "unrelated_temp" {
				if err != nil || dump == nil {
					t.Fatalf("harmless TEMP rejected: %v", err)
				}
			} else if !errors.Is(err, backup.ErrLegacyHeadReceiptExportUnsupported) || dump != nil {
				t.Fatalf("selected TEMP shadow accepted: %v", err)
			}
		})
	}
}

func TestSyntheticReceiptExportRequiredProofTextIsTransportSafe(t *testing.T) {
	for _, column := range []string{"id", "definition_hash", "created_at"} {
		t.Run(column, func(t *testing.T) {
			source, ws := syntheticReceiptExportFixture(t, nil)
			// Explicit synthetic storage corruption control, not claimed historical proof.
			if _, err := source.Exec(`UPDATE pipeline_legacy_head_receipts SET ` + column + `=CAST(X'ff' AS TEXT)`); err != nil {
				t.Fatal(err)
			}
			before := syntheticReceiptExportState(t, source)
			dump, err := backup.DumpWorkspace(t.Context(), source, ws)
			if !errors.Is(err, backup.ErrLegacyHeadReceiptExportUnsupported) || dump != nil {
				t.Fatalf("unsafe proof text accepted: %v", err)
			}
			if !reflect.DeepEqual(before, syntheticReceiptExportState(t, source)) {
				t.Fatal("refusal changed corrupt metadata")
			}
		})
	}
}
