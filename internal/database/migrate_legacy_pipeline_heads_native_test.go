package database

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// Source-only counterfactual: legal native BLOBs are not the old Go writer's
// TEXT capture shape, even if their bytes have the same valid SHA256.
func TestLegacyCurrentHeadCaptureRequiresNativeWriterShape(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("SETUP: actual foreign keys required: %d %v", enabled, err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	foundation := 0
	for _, m := range migrations {
		if m.name == "pipeline_version_authority_identity" {
			foundation = m.version
		}
	}
	if foundation == 0 {
		t.Fatal("SETUP: actual authority foundation absent")
	}
	if err := applyMigrationsUpTo(t.Context(), db, foundation, logger); err != nil {
		t.Fatalf("SETUP: foundation: %v", err)
	}
	mustExecCtx(t, db, t.Context(), `INSERT INTO workspaces(id,name,slug) VALUES('g1-native-w','Native','g1-native-w')`)
	raw := " {\"name\":\"native old\",\"steps\":[]} \n"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	for i, origin := range []any{"g1-origin-text", []byte("g1-origin-blob")} {
		mustExecCtx(t, db, t.Context(), `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES(?,'g1-native-w',?,'chat','message','{}','2026-10-01T00:00:00Z')`, origin, fmt.Sprintf("root-%d", i))
	}
	types := []struct {
		label                       string
		id, raw, hash, head, origin any
		kinds                       [5]string
		capture                     bool
	}{
		{"text-null", "g1-text-null", raw, hash, 7, nil, [5]string{"text", "integer", "text", "text", "null"}, true},
		{"text-origin", "g1-text-origin", raw, hash, 7, "g1-origin-text", [5]string{"text", "integer", "text", "text", "text"}, true},
		{"raw", "g1-raw", []byte(raw), hash, 7, nil, [5]string{"text", "integer", "blob", "text", "null"}, false},
		{"hash", "g1-hash", raw, []byte(hash), 7, nil, [5]string{"text", "integer", "text", "blob", "null"}, false},
		{"both", "g1-both", []byte(raw), []byte(hash), 7, nil, [5]string{"text", "integer", "blob", "blob", "null"}, false},
		{"id", []byte("g1-id"), raw, hash, 7, nil, [5]string{"blob", "integer", "text", "text", "null"}, false},
		{"head", "g1-head", raw, hash, []byte("7"), nil, [5]string{"text", "blob", "text", "text", "null"}, false},
		{"origin", "g1-origin", raw, hash, 7, []byte("g1-origin-blob"), [5]string{"text", "integer", "text", "text", "blob"}, false},
	}
	before := map[string][]any{}
	for _, tc := range types {
		mustExecCtx(t, db, t.Context(), `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version,chat_origin_id) VALUES(?,'g1-native-w',?,'Native fixture',?,?,?,?)`, tc.id, tc.label, tc.raw, tc.hash, tc.head, tc.origin)
		var kinds [5]string
		if err := db.QueryRow(`SELECT typeof(id),typeof(head_version),typeof(definition_json),typeof(definition_hash),typeof(chat_origin_id) FROM main.pipelines WHERE id=?`, tc.id).Scan(&kinds[0], &kinds[1], &kinds[2], &kinds[3], &kinds[4]); err != nil || kinds != tc.kinds {
			t.Fatalf("SETUP: required native kinds absent for %s: %v want %v %v", tc.label, kinds, tc.kinds, err)
		}
		before[tc.label] = legacyHeadNativeRow(t, db, tc.id)
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("SETUP: native fixtures must obey actual FKs: %d %v", violations, err)
	}
	if err := Migrate(t.Context(), db, logger); err != nil {
		t.Fatalf("cutover must complete while leaving unsupported native shapes uncaptured: %v", err)
	}
	for _, tc := range types {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM main.pipeline_versions WHERE pipeline_id=?`, tc.id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if tc.capture {
			want = 1
		}
		if count != want {
			t.Errorf("native %s source captured=%d want=%d", tc.label, count, want)
		}
		if tc.capture && count == 1 {
			var gotRaw, gotHash, gotHead, gotOrigin any
			if err := db.QueryRow(`SELECT definition_json,definition_hash,version,chat_origin_id FROM main.pipeline_versions WHERE pipeline_id=?`, tc.id).Scan(&gotRaw, &gotHash, &gotHead, &gotOrigin); err != nil || gotRaw != raw || gotHash != hash || gotHead != int64(7) || !reflect.DeepEqual(gotOrigin, tc.origin) {
				t.Errorf("native positive archive changed bytes/types for %s: %#v %#v %#v %#v %v", tc.label, gotRaw, gotHash, gotHead, gotOrigin, err)
			}
		}
		if !reflect.DeepEqual(before[tc.label], legacyHeadNativeRow(t, db, tc.id)) {
			t.Errorf("cutover rewrote original native source %s", tc.label)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("cutover left foreign-key violations: %d %v", violations, err)
	}
}

func legacyHeadNativeRow(t *testing.T, db *sql.DB, id any) []any {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT * FROM main.pipelines WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || !rows.Next() {
		t.Fatalf("SETUP: native source row absent: %v", err)
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(values))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		t.Fatal(err)
	}
	for i, value := range values {
		if b, ok := value.([]byte); ok {
			copyBytes := make([]byte, len(b))
			copy(copyBytes, b)
			values[i] = copyBytes
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("SETUP: native source row not singular/readable")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return values
}
