package testutil

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

// These fresh-migration timestamps are assigned by the runner / feature flag
// defaults, not supplied auth/content values. v50 inserts the singleton
// instance_config row with its datetime(now) installed_at default (and v144
// explicitly preserves that write-only bookkeeping timestamp). Every other
// column, including instance_config.hostname, is compared.
var fixtureMigrationClockColumns = map[string]map[string]bool{
	"_migrations":     {"applied_at": true},
	"feature_flags":   {"created_at": true, "updated_at": true},
	"instance_config": {"installed_at": true},
}

func snapshotFixtureRows(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	names, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var name string
		if err = names.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err = names.Err(); err != nil {
		t.Fatal(err)
	}
	_ = names.Close()
	result := map[string][]string{}
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		rows, err := db.Query(`SELECT * FROM ` + quoted)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		result[table] = []string{}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, column := range columns {
				if !fixtureMigrationClockColumns[table][column] {
					continue
				}
				value, ok := values[i].(string)
				if !ok {
					t.Fatalf("migration timestamp %s.%s is not text", table, column)
				}
				parsed, err := time.Parse("2006-01-02 15:04:05", value)
				if err != nil {
					parsed, err = time.Parse(time.RFC3339Nano, value)
				}
				if err != nil || parsed.IsZero() {
					t.Fatalf("migration timestamp %s.%s is not a valid documented migration timestamp", table, column)
				}
				values[i] = "<fresh migration clock>"
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			result[table] = append(result[table], string(encoded))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		sort.Strings(result[table])
	}
	return result
}

func TestNormalTemplateHelperFullImageMatchesInProcessMigrateAndFullConsumer(t *testing.T) {
	// Explicit GOFLAGS race must be overridden, while all other flags survive.
	t.Setenv("GOFLAGS", os.Getenv("GOFLAGS")+" -race")
	normalPath := filepath.Join(t.TempDir(), "normal.db")
	plan, reason, err := normalTemplatePlan()
	if err != nil || reason != "" {
		t.Fatalf("checked-out source and running SDK must exercise actual helper: %q %v", reason, err)
	}
	first := time.Now()
	if err = plan.buildFixture(t.Context(), t.TempDir(), normalPath); err != nil {
		t.Fatal(err)
	}
	t.Logf("normal helper including first compilation, verification and full Migrate: %s", time.Since(first))
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(normalPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("builder returned before closed/checkpointed single-file publication: %s %v", suffix, err)
		}
	}
	openFull := func(path string) *database.DB {
		db, err := database.Open("file:"+path, database.WithSynchronous(database.SynchronousFull))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	normal := openFull(normalPath)
	referenceSQL := MigratedSQLDB(t)
	if !reflect.DeepEqual(objectNames(t, normal.DB), objectNames(t, referenceSQL)) {
		t.Fatal("normal helper differs from complete in-process migrated schema")
	}
	left, right := snapshotFixtureRows(t, normal.DB), snapshotFixtureRows(t, referenceSQL)
	if !reflect.DeepEqual(left, right) {
		for table, rows := range left {
			if !reflect.DeepEqual(rows, right[table]) {
				t.Errorf("fresh migration rows differ in table %s (normal=%d reference=%d); auth/content columns must not be waived", table, len(rows), len(right[table]))
			}
		}
		t.FailNow()
	}
	for _, db := range []*sql.DB{normal.DB, referenceSQL} {
		check, err := db.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		if check.Next() {
			t.Fatal("complete migrated fixture violates foreign keys")
		}
		if err = check.Err(); err != nil {
			t.Fatal(err)
		}
		_ = check.Close()
	}
	clonePath := filepath.Join(t.TempDir(), "clone.db")
	if err = copyFileForTest(normalPath, clonePath); err != nil {
		t.Fatal(err)
	}
	clone := openFull(clonePath)
	if _, err = normal.Exec(`INSERT INTO users(id,email,full_name) VALUES('helper-isolated','helper-isolated@example.test','Fixture')`); err != nil {
		t.Fatal(err)
	}
	for _, other := range []*sql.DB{clone.DB, referenceSQL} {
		var count int
		if err = other.QueryRow(`SELECT count(*) FROM users WHERE id='helper-isolated'`).Scan(&count); err != nil || count != 0 {
			t.Fatal("fixture rows leaked across private files")
		}
	}
	var held []*sql.Conn
	defer func() {
		for _, conn := range held {
			_ = conn.Close()
		}
	}()
	for i := 0; i < 5; i++ {
		conn, err := normal.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		for query, want := range map[string]int{"PRAGMA synchronous": 2, "PRAGMA foreign_keys": 1, "PRAGMA busy_timeout": 30000} {
			var got int
			if err = conn.QueryRowContext(t.Context(), query).Scan(&got); err != nil || got != want {
				t.Fatalf("FULL consumer connection%d %s=%d want%d: %v", i, query, got, want, err)
			}
		}
		_, err = conn.ExecContext(t.Context(), `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('missing-workspace','missing-user','OWNER')`)
		if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY") {
			t.Fatalf("FULL consumer connection%d did not enforce real schema FK: %v", i, err)
		}
	}
	if normal.Stats().MaxOpenConnections != 5 {
		t.Fatal("FULL consumer production pool capacity changed")
	}
	t.Logf("matched every schema object and table row on %s/%s; only enumerated migration clock columns normalized", runtime.GOOS, runtime.GOARCH)
}

func TestNormalTemplateHelperUnavailableSourceAndSDKAreExplicit(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		t.Chdir(t.TempDir())
		_, reason, err := normalTemplatePlan()
		if err != nil || reason == "" {
			t.Fatalf("expected explicit missing-source fallback reason: %q %v", reason, err)
		}
	})
	t.Run("SDK", func(t *testing.T) {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestNormalTemplateUnavailableSDKWorker$", "-test.count=1")
		cmd.Env = replaceNormalHelperEnv(os.Environ(), "GOROOT", filepath.Join(t.TempDir(), "missing-sdk"))
		cmd.Env = replaceNormalHelperEnv(cmd.Env, "CREWSHIP_TEST_MISSING_SDK_CHECK", "1")
		existing := filepath.Join(t.TempDir(), "existing.db")
		if err = os.WriteFile(existing, []byte("existing fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd.Env = replaceNormalHelperEnv(cmd.Env, "CREWSHIP_TEST_MISSING_SDK_EXISTING", existing)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("missing-SDK detection failed: %s %v", out, err)
		}
	})
}

func TestNormalTemplateUnavailableSDKWorker(t *testing.T) {
	output := os.Getenv("CREWSHIP_TEST_MISSING_SDK_EXISTING")
	if output == "" && os.Getenv("CREWSHIP_TEST_MISSING_SDK_CHECK") == "" {
		return
	}
	_, reason, err := normalTemplatePlan()
	if err != nil || !strings.Contains(reason, "SDK") {
		t.Fatalf("expected explicit missing-SDK fallback reason: %q %v", reason, err)
	}
	if output != "" {
		before, readErr := os.ReadFile(output)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err = migrateTemplateAt(output); err == nil {
			t.Fatal("missing-SDK fallback did not invoke the full fresh builder guard")
		}
		after, readErr := os.ReadFile(output)
		if readErr != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("fallback changed an existing fixture")
		}
	}
}
