package skills_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
)

const lockedSkillV2 = `---
name: test-skill
display_name: Test Skill
version: 2.0.0
author: rewriter
description: A rewritten body.
category: CUSTOM
---
# Test Skill

REWRITTEN BY AUTOMATION`

func lockSkillRow(t *testing.T, imp *skills.Importer, dbExec func(string, ...any), locked bool) string {
	t.Helper()
	res, err := imp.Import(context.Background(), "ws", "u", skills.ImportRequest{Content: validSkillMD})
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		dbExec(`UPDATE skills SET locked = 1 WHERE id = ?`, res.SkillID)
	}
	return res.SkillID
}

// Every importer write path (single import, which the proposed-approve flow
// also uses, and bulk import) refuses to rewrite a locked skill and names the
// lock; the unlocked control still updates.
func TestImporter_LockedSkillContentIsNeverRewritten(t *testing.T) {
	for _, locked := range []bool{true, false} {
		name := "unlocked control"
		if locked {
			name = "locked"
		}
		t.Run(name, func(t *testing.T) {
			db := setupSkillTestDB(t)
			imp := skills.NewImporter(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
			id := lockSkillRow(t, imp, func(q string, a ...any) {
				if _, err := db.Exec(q, a...); err != nil {
					t.Fatal(err)
				}
			}, locked)

			_, err := imp.Import(context.Background(), "ws", "u", skills.ImportRequest{Content: lockedSkillV2, SourceType: "GENERATED"})
			var content string
			if e := db.QueryRow(`SELECT content FROM skills WHERE id = ?`, id).Scan(&content); e != nil {
				t.Fatal(e)
			}
			if locked {
				var le *skills.LockedError
				if !errors.As(err, &le) || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "test-skill") {
					t.Fatalf("err = %v, want LockedError naming the lock and slug", err)
				}
				if strings.Contains(content, "REWRITTEN") {
					t.Fatalf("locked content was rewritten: %q", content)
				}
				return
			}
			if err != nil || !strings.Contains(content, "REWRITTEN") {
				t.Fatalf("unlocked skill should update: err=%v content=%q", err, content)
			}
		})
	}
}

func TestBulkImport_LockedSkillIsSkippedAndFlagged(t *testing.T) {
	db := setupSkillTestDB(t)
	imp := skills.NewImporter(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	id := lockSkillRow(t, imp, func(q string, a ...any) { _, _ = db.Exec(q, a...) }, true)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "test-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test-skill", "SKILL.md"), []byte(strings.Replace(lockedSkillV2, "category: CUSTOM", "category: CUSTOM\nlicense: MIT", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := imp.BulkImport(context.Background(), skills.BulkImportRequest{LocalPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skills) != 0 || len(res.Skipped) != 1 || !res.Skipped[0].Locked {
		t.Fatalf("want one locked skip, got imported=%d skipped=%+v", len(res.Skills), res.Skipped)
	}
	var content string
	_ = db.QueryRow(`SELECT content FROM skills WHERE id = ?`, id).Scan(&content)
	if strings.Contains(content, "REWRITTEN") {
		t.Fatal("bulk import rewrote a locked skill")
	}
}

func TestStageAuthoredSkillGuarded(t *testing.T) {
	db := setupSkillTestDB(t)
	imp := skills.NewImporter(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	lockSkillRow(t, imp, func(q string, a ...any) { _, _ = db.Exec(q, a...) }, true)

	dir := t.TempDir()
	_, err := skills.StageAuthoredSkillGuarded(context.Background(), db, dir, lockedSkillV2)
	if !skills.IsLocked(err) {
		t.Fatalf("err = %v, want locked", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a locked slug must not be staged, found %d files", len(entries))
	}
	other := strings.Replace(lockedSkillV2, "name: test-skill", "name: another-skill", 1)
	if _, err := skills.StageAuthoredSkillGuarded(context.Background(), db, dir, other); err != nil {
		t.Fatalf("unlocked slug should stage: %v", err)
	}
}
