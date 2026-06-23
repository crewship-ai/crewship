package bundled

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/skills"
)

func covLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTagsToJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, `[]`},
		{"single", []string{"coding"}, `["coding"]`},
		{"multiple", []string{"a", "b", "c"}, `["a","b","c"]`},
		{"quote escaped", []string{`he said "hi"`}, `["he said \"hi\""]`},
		{"backslash escaped", []string{`a\b`}, `["a\\b"]`},
		{"unicode passthrough", []string{"héllo🦀"}, `["héllo🦀"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tagsToJSON(tc.in)
			if got != tc.want {
				t.Errorf("tagsToJSON(%v) = %s, want %s", tc.in, got, tc.want)
			}
			// The output must be valid JSON that round-trips to the input.
			var back []string
			if err := json.Unmarshal([]byte(got), &back); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}
			if len(back) != len(tc.in) {
				t.Fatalf("round-trip length = %d, want %d", len(back), len(tc.in))
			}
			for i := range back {
				if back[i] != tc.in[i] {
					t.Errorf("round-trip[%d] = %q, want %q", i, back[i], tc.in[i])
				}
			}
		})
	}
}

func TestHumaniseSlug(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"skill-creator", "Skill Creator"},
		{"mcp-builder", "Mcp Builder"},
		{"single", "Single"},
		{"a--b", "A  B"}, // empty middle part is skipped, join keeps the gap
	}
	for _, tc := range cases {
		if got := humaniseSlug(tc.in); got != tc.want {
			t.Errorf("humaniseSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGenerateBundledID(t *testing.T) {
	t.Parallel()
	a := generateBundledID("anthropic", "skill-creator")
	b := generateBundledID("anthropic", "skill-creator")
	for _, id := range []string{a, b} {
		if !strings.HasPrefix(id, "sk_") {
			t.Errorf("id %q missing sk_ prefix", id)
		}
		if len(id) != len("sk_")+24 { // 12 random bytes hex-encoded
			t.Errorf("id %q length = %d, want %d", id, len(id), len("sk_")+24)
		}
	}
	if a == b {
		t.Error("two generated IDs collided — entropy not applied")
	}
}

// minimalSkillsSchema is just enough of the skills table for upsert to
// work. Kept local so this test exercises upsert in isolation without
// the full migration chain.
const minimalSkillsSchema = `
CREATE TABLE skills (
    id TEXT PRIMARY KEY,
    name TEXT, slug TEXT, display_name TEXT, description TEXT,
    version TEXT, author TEXT, license TEXT, category TEXT,
    source TEXT, icon TEXT, content TEXT, tags TEXT,
    vendor TEXT, homepage TEXT, spdx_license TEXT, runtime TEXT,
    maturity TEXT, scan_status TEXT, description_quality TEXT,
    verification TEXT, pricing_tier TEXT,
    created_at TEXT, updated_at TEXT,
    UNIQUE(vendor, slug)
);`

func openMinimalDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(minimalSkillsSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestUpsert_InsertDefaultsAndTags(t *testing.T) {
	t.Parallel()
	db := openMinimalDB(t)
	ctx := context.Background()

	parsed := &skills.ParsedSkill{
		Meta: skills.SkillMeta{
			Description: "does things",
			Tags:        []string{"alpha", "beta"},
			// DisplayName and Version intentionally empty → defaults apply.
		},
		Content: "# body",
	}
	vmeta := vendors["anthropic"]
	man := skillManifest{category: "CODING", icon: "sparkles"}

	if err := upsert(ctx, db, parsed, "anthropic", "my-skill", vmeta, man, "2026-06-11T00:00:00Z"); err != nil {
		t.Fatalf("upsert insert: %v", err)
	}

	var displayName, version, tags, homepage, source string
	err := db.QueryRow(`SELECT display_name, version, tags, homepage, source
		FROM skills WHERE vendor='anthropic' AND slug='my-skill'`).
		Scan(&displayName, &version, &tags, &homepage, &source)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if displayName != "My Skill" {
		t.Errorf("display_name = %q, want humanised 'My Skill'", displayName)
	}
	if version != "1.0.0" {
		t.Errorf("version = %q, want default 1.0.0", version)
	}
	if tags != `["alpha","beta"]` {
		t.Errorf("tags = %s, want [\"alpha\",\"beta\"]", tags)
	}
	if homepage != vmeta.homepageRoot+"my-skill" {
		t.Errorf("homepage = %q", homepage)
	}
	if source != "BUNDLED" {
		t.Errorf("source = %q, want BUNDLED", source)
	}
}

func TestUpsert_UpdateExistingRow(t *testing.T) {
	t.Parallel()
	db := openMinimalDB(t)
	ctx := context.Background()
	vmeta := vendors["anthropic"]
	man := skillManifest{category: "CODING", icon: "sparkles"}

	first := &skills.ParsedSkill{
		Meta:    skills.SkillMeta{Description: "v1 description", Version: "1.0.0"},
		Content: "# v1",
	}
	if err := upsert(ctx, db, first, "anthropic", "up-skill", vmeta, man, "2026-06-10T00:00:00Z"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	second := &skills.ParsedSkill{
		Meta:    skills.SkillMeta{Description: "v2 description", Version: "2.0.0", DisplayName: "Upgraded"},
		Content: "# v2",
	}
	if err := upsert(ctx, db, second, "anthropic", "up-skill", vmeta, man, "2026-06-11T00:00:00Z"); err != nil {
		t.Fatalf("update: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE slug='up-skill'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1 (update, not duplicate insert)", count)
	}
	var desc, version, displayName, updatedAt string
	if err := db.QueryRow(`SELECT description, version, display_name, updated_at
		FROM skills WHERE slug='up-skill'`).Scan(&desc, &version, &displayName, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if desc != "v2 description" || version != "2.0.0" || displayName != "Upgraded" {
		t.Errorf("row not refreshed: desc=%q version=%q display=%q", desc, version, displayName)
	}
	if updatedAt != "2026-06-11T00:00:00Z" {
		t.Errorf("updated_at = %q, want refreshed timestamp", updatedAt)
	}
}

func TestUpsert_InsertErrorSurfaces(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Table exists for the SELECT but lacks most columns → INSERT fails.
	if _, err := db.Exec(`CREATE TABLE skills (id TEXT, vendor TEXT, slug TEXT)`); err != nil {
		t.Fatal(err)
	}
	parsed := &skills.ParsedSkill{Meta: skills.SkillMeta{}, Content: "x"}
	err = upsert(context.Background(), db, parsed, "anthropic", "broken",
		vendors["anthropic"], skillManifest{category: "CODING"}, "2026-06-11T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "insert:") {
		t.Errorf("err = %v, want insert error", err)
	}
}

func TestUpsert_LookupErrorSurfaces(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// No skills table at all → the SELECT errors (not ErrNoRows).
	parsed := &skills.ParsedSkill{Meta: skills.SkillMeta{}, Content: "x"}
	err = upsert(context.Background(), db, parsed, "anthropic", "nope",
		vendors["anthropic"], skillManifest{category: "CODING"}, "2026-06-11T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "lookup existing") {
		t.Errorf("err = %v, want lookup error", err)
	}
}

func TestUpsert_UpdateErrorSurfaces(t *testing.T) {
	t.Parallel()
	db := openMinimalDB(t)
	ctx := context.Background()
	vmeta := vendors["anthropic"]
	man := skillManifest{category: "CODING"}

	parsed := &skills.ParsedSkill{Meta: skills.SkillMeta{}, Content: "x"}
	if err := upsert(ctx, db, parsed, "anthropic", "trig-skill", vmeta, man, "2026-06-10T00:00:00Z"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Sabotage updates only — the lookup still succeeds.
	if _, err := db.Exec(`CREATE TRIGGER fail_update BEFORE UPDATE ON skills
		BEGIN SELECT RAISE(ABORT, 'updates disabled'); END`); err != nil {
		t.Fatal(err)
	}
	err := upsert(ctx, db, parsed, "anthropic", "trig-skill", vmeta, man, "2026-06-11T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "update:") {
		t.Errorf("err = %v, want update error", err)
	}
}

// TestInstall_MissingTableContinuesWithoutFatal proves the per-skill
// best-effort contract: a broken DB makes every upsert fail, but Install
// still returns nil (losing bundled skills must not block startup).
func TestInstall_MissingTableContinuesWithoutFatal(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Install(context.Background(), db, covLogger()); err != nil {
		t.Errorf("Install on schemaless DB = %v, want nil (best-effort)", err)
	}
}
