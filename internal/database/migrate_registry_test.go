package database

import (
	"strings"
	"testing"
)

// The registry is built in init(), where an error cannot be returned. This is
// the gate that turns a malformed embedded migration into a red build rather
// than a server that refuses to start.
func TestMigrationRegistryBuildsCleanly(t *testing.T) {
	if migrationRegistryErr != nil {
		t.Fatalf("migration registry failed to build: %v", migrationRegistryErr)
	}
	if len(migrations) < len(legacyMigrations) {
		t.Fatalf("registry has %d entries, fewer than the %d legacy ones — the merge dropped some",
			len(migrations), len(legacyMigrations))
	}
}

// A file colliding with a legacy entry is the case loadFileMigrations cannot
// see on its own, so the merge has to check it.
func TestBuildMigrationRegistry_RejectsCollisionsAcrossTheBoundary(t *testing.T) {
	// A legacy entry that reuses a version the file loader would also produce
	// stands in for "somebody added a file whose stamp is already taken".
	dup := []migration{
		{version: 20260728143000, name: "one", sql: "SELECT 1"},
		{version: 20260728143000, name: "two", sql: "SELECT 1"},
	}
	if _, err := buildMigrationRegistry(dup); err == nil ||
		!strings.Contains(err.Error(), "claimed twice") {
		t.Errorf("err = %v, want a version-collision refusal", err)
	}

	sameName := []migration{
		{version: 20260728143000, name: "same", sql: "SELECT 1"},
		{version: 20260728150000, name: "same", sql: "SELECT 1"},
	}
	if _, err := buildMigrationRegistry(sameName); err == nil ||
		!strings.Contains(err.Error(), "is used at versions") {
		t.Errorf("err = %v, want a name-collision refusal", err)
	}
}

// Ordering is what makes the two-era scheme work: files carry timestamps, the
// legacy block carries small integers, and the merged result has to be
// strictly ascending or migrations would apply out of order.
func TestBuildMigrationRegistry_MergesInVersionOrder(t *testing.T) {
	reg, err := buildMigrationRegistry(legacyMigrations)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for i := 1; i < len(reg); i++ {
		if reg[i].version <= reg[i-1].version {
			t.Fatalf("registry not ascending at index %d: v%d (%s) after v%d (%s)",
				i, reg[i].version, reg[i].name, reg[i-1].version, reg[i-1].name)
		}
	}
}

// Every rule the loader enforces, driven through the real parser rather than
// asserted about it. These are the mistakes a hurried author actually makes.
func TestLoadFileMigrations_FilenameRules(t *testing.T) {
	// loadFileMigrations reads the embedded FS, so the rules are checked by
	// calling the validator the loader uses on the same inputs it would see.
	cases := []struct {
		file    string
		wantErr string
	}{
		{"20260728143000_add_widget_flag.sql", ""},
		{"20260728143000-add-widget-flag.sql", "does not match"},
		{"add_widget_flag.sql", "does not match"},
		{"20260728143000_AddWidgetFlag.sql", "does not match"},
		// 170 is above the ceiling, so the useful advice is "use a
		// timestamp", not "that block is closed".
		{"170_add_widget_flag.sql", "not a YYYYMMDDHHMMSS timestamp"},
		{"1_init.sql", "legacy block"},
		{"20261340000000_bad_date.sql", "not a valid YYYYMMDDHHMMSS"},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			err := checkMigrationFilename(tc.file)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkMigrationFilename(%q) = %v, want nil", tc.file, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkMigrationFilename(%q) = nil, want %q", tc.file, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The directories exist and are documented — a contributor finding an empty
// tree with no README is how the convention gets ignored.
func TestMigrationDirectoriesAreDocumented(t *testing.T) {
	for _, p := range []string{"migrations/README.md", "migrations/post_deploy/README.md"} {
		b, err := migrationFS.ReadFile(p)
		if err != nil {
			t.Errorf("%s is missing from the embedded tree: %v", p, err)
			continue
		}
		if len(strings.TrimSpace(string(b))) < 200 {
			t.Errorf("%s is too short to explain the convention it exists to explain", p)
		}
	}
}
