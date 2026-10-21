package database

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// Every row attempt is isolated so an accepted malformed value cannot mask
// another case through an unrelated uniqueness constraint.
func TestPipelineVersionAuthorityRequiresExactTextBytes(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db := migrationBoundaryDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, migrations[len(migrations)-1].version, logger); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('authority-bytes-ws','Fixture','authority-bytes-ws')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('authority-bytes-p','authority-bytes-ws','fixture','Fixture','{}','bytes')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"legacy-text", "legacy-trusted:v1", true},
		{"hex-text", strings.Repeat("a", 64), true},
		{"hex-nul-tail", strings.Repeat("a", 64) + "\x00suffix", false},
		{"nul-text", "\x00", false},
		{"legacy-nul-tail", "legacy-trusted:v1\x00suffix", false},
		{"hex-blob", []byte(strings.Repeat("a", 64)), false},
		{"legacy-blob", []byte("legacy-trusted:v1"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = tx.ExecContext(t.Context(), `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint) VALUES('bytes-version','authority-bytes-p',1,'{}','bytes','user','fixture',?)`, tc.value)
			if tc.valid && err != nil {
				t.Fatalf("valid exact text rejected: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("noncanonical fingerprint accepted: type=%T value=%q", tc.value, tc.value)
			}
		})
	}
}
