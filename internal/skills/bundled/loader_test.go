package bundled_test

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/skills/bundled"
)

// TestEmbedFS_ContainsAnthropicSkills proves the //go:embed directive
// pulled the SKILL.md files into the binary at compile time. Catches the
// "forgot to commit the bundled/ folder" failure mode early.
func TestEmbedFS_ContainsAnthropicSkills(t *testing.T) {
	t.Parallel()
	want := []string{
		"anthropic/skill-creator/SKILL.md",
		"anthropic/mcp-builder/SKILL.md",
		"anthropic/claude-api/SKILL.md",
		"anthropic/frontend-design/SKILL.md",
	}
	for _, p := range want {
		f, err := fs.ReadFile(bundled.FS(), p)
		if err != nil {
			t.Errorf("missing embedded file %s: %v", p, err)
			continue
		}
		if !strings.HasPrefix(string(f), "---") {
			t.Errorf("%s does not start with frontmatter delimiter", p)
		}
	}
}

// TestInstall_PopulatesSkillsTable verifies the loader walks the FS,
// parses each SKILL.md, and writes a row per skill with the bundled
// metadata applied (vendor, license, maturity).
func TestInstall_PopulatesSkillsTable(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	if err := database.Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := bundled.Install(context.Background(), db, logger); err != nil {
		t.Fatalf("install: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE vendor = 'anthropic'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count < 12 {
		t.Errorf("expected at least 12 anthropic skills, got %d", count)
	}

	// Sample one row to confirm columns populated.
	var slug, vendor, spdx, maturity, scanStatus, source string
	err = db.QueryRow(`
		SELECT slug, vendor, spdx_license, maturity, scan_status, source
		FROM skills WHERE vendor = 'anthropic' AND slug = 'skill-creator'
	`).Scan(&slug, &vendor, &spdx, &maturity, &scanStatus, &source)
	if err != nil {
		t.Fatalf("sample row: %v", err)
	}
	if vendor != "anthropic" || spdx != "Apache-2.0" || maturity != "OFFICIAL" ||
		scanStatus != "CLEAN" || source != "BUNDLED" {
		t.Errorf("metadata mismatch: vendor=%s spdx=%s maturity=%s scan=%s source=%s",
			vendor, spdx, maturity, scanStatus, source)
	}
}

// TestInstall_Idempotent confirms repeated calls don't duplicate rows.
// The (vendor, slug) lookup in upsert() is what guarantees this; if the
// query ever regresses to slug-only the count would balloon.
func TestInstall_Idempotent(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	if err := database.Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := bundled.Install(context.Background(), db, logger); err != nil {
		t.Fatalf("install 1: %v", err)
	}
	var first int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE vendor = 'anthropic'`).Scan(&first); err != nil {
		t.Fatalf("count after first install: %v", err)
	}
	if err := bundled.Install(context.Background(), db, logger); err != nil {
		t.Fatalf("install 2: %v", err)
	}
	var second int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE vendor = 'anthropic'`).Scan(&second); err != nil {
		t.Fatalf("count after second install: %v", err)
	}
	if first != second {
		t.Errorf("idempotency broken: %d -> %d", first, second)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
