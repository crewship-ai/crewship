package backup_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Snapshot every actual table, including global/non-FK monotonic evidence.
// Sort encoded rows instead of assuming all tables have a rowid/id column.
func publicationRestoreWholeDB(t *testing.T, db *sql.DB) [32]byte {
	t.Helper()
	names, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var name string
		if err := names.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := errors.Join(names.Err(), names.Close()); err != nil {
		t.Fatal(err)
	}
	all := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		all[table] = []string{}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range ptrs {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			all[table] = append(all[table], string(raw))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		sort.Strings(all[table])
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func TestPipelineVersionAuthorityExportMetadataAndLegacyRoundtrip(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	fingerprint := strings.Repeat("a", 64)
	if _, err := source.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,chat_origin_id,authority_fingerprint) SELECT 'bounded-identity',pipeline_id,3,definition_json,definition_hash,author_type,author_id,chat_origin_id,? FROM pipeline_versions WHERE id='origin-version'`, fingerprint); err != nil {
		t.Fatal(err)
	}
	sourceBefore := publicationRestoreWholeDB(t, source)
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["pipeline_versions"]) != 3 {
		t.Fatal("equal content collapsed distinct identities during export")
	}
	check := func(db *sql.DB, id, expected string, version int) {
		t.Helper()
		var actual, origin string
		var number int
		if err := db.QueryRow(`SELECT authority_fingerprint,version,COALESCE(chat_origin_id,'') FROM pipeline_versions WHERE id=?`, id).Scan(&actual, &number, &origin); err != nil {
			t.Fatal(err)
		}
		if actual != expected || number != version || origin == "" {
			t.Fatalf("lost immutable identity: %q %d %q", actual, number, origin)
		}
	}
	check(source, "origin-version", "legacy-trusted:v1", 1)
	check(source, "bounded-identity", fingerprint, 3)
	target := openMigratedDB(t)

	// Remapping still preserves exact integer/fingerprint/origin graph metadata;
	// this is not an assertion that unqualified bounded import may execute it.
	if err := backup.RemapIDs(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	dump.Tables["workspaces"][0]["slug"] = "authority-identity-fork"
	boundedFound := 0
	for _, row := range dump.Tables["pipeline_versions"] {
		if row["authority_fingerprint"] == fingerprint {
			boundedFound++
			id, ok := row["id"].(string)
			if !ok || id == "bounded-identity" {
				t.Fatal("fork did not remap version ID")
			}
			if fmt.Sprint(row["version"]) != "3" {
				t.Fatal("fork changed immutable integer version")
			}
			origin, ok := row["chat_origin_id"].(string)
			if !ok || origin == "" {
				t.Fatal("fork lost exact nonempty archive origin")
			}
			found := false
			for _, receipt := range dump.Tables["execution_origins"] {
				if receipt["id"] == origin {
					found = true
				}
			}
			if !found {
				t.Fatal("fork archive origin is not the exact mapped receipt")
			}
		}
	}
	if boundedFound != 1 {
		t.Fatalf("fork lost bounded authority identity: found %d", boundedFound)
	}
	if publicationRestoreWholeDB(t, source) != sourceBefore {
		t.Fatal("export/remap/rejection created source evidence or changed source authority")
	}
	// Genuine legacy rows alone retain both normal and forked roundtrip. Missing
	// discriminator never reclassifies bounded source rows as old legacy input.
	legacy, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	var oldRows []map[string]any
	for _, row := range legacy.Tables["pipeline_versions"] {
		if row["authority_fingerprint"] == "legacy-trusted:v1" {
			delete(row, "authority_fingerprint")
			oldRows = append(oldRows, row)
		}
	}
	legacy.Tables["pipeline_versions"] = oldRows
	if err := backup.RestoreDump(t.Context(), target, legacy); err != nil {
		t.Fatal(err)
	}
	checkLegacy := func() {
		t.Helper()
		for _, row := range legacy.Tables["pipeline_versions"] {
			var got string
			var origin sql.NullString
			var version int
			err := target.QueryRow(`SELECT authority_fingerprint,version,chat_origin_id FROM pipeline_versions WHERE id=?`, row["id"]).Scan(&got, &version, &origin)
			expected, hasOrigin := row["chat_origin_id"].(string)
			originMatches := origin.Valid == hasOrigin && (!origin.Valid || origin.String == expected)
			if err != nil || got != "legacy-trusted:v1" || fmt.Sprint(version) != fmt.Sprint(row["version"]) || !originMatches {
				t.Fatalf("legacy identity/origin roundtrip: %q %d %#v %v", got, version, origin, err)
			}
		}
	}
	checkLegacy()
	if err := backup.RemapIDs(t.Context(), target, legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Tables["workspaces"][0]["slug"] = "authority-identity-legacy"
	if err := backup.RestoreDump(t.Context(), target, legacy); err != nil {
		t.Fatal(err)
	}
	checkLegacy()
}
