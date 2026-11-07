package consolidate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/skills"
)

type blockedRecorder struct{ entries []journal.Entry }

func (b *blockedRecorder) Emit(_ context.Context, e journal.Entry) (string, error) {
	b.entries = append(b.entries, e)
	return "id", nil
}
func (b *blockedRecorder) Flush(context.Context) error { return nil }

// The memory-to-skill bridge never stages a rewrite of a locked skill, and the
// blocked promotion is journaled (#5045).
func TestPromoteProposalSkills_LockedSlugIsNotStaged(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS skills (slug TEXT, locked INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	rec := &blockedRecorder{}
	c := &Consolidator{DB: db, Journal: rec, Logger: quietLogger()}
	rules := []LearnedRule{{Pattern: "high-recall stable rule", Action: "do canonical thing", Evidence: []string{"e1"}}}
	scores := map[string]ScoreResult{"high-recall stable rule": {Composite: 0.90, RecallCount: 14, Promoted: true}}
	slug := skills.Slugify("high-recall stable rule")

	tests := []struct {
		name       string
		locked     int
		wantStaged int
		wantBlock  int
	}{
		{"unlocked slug is staged", 0, 1, 0},
		{"locked slug is refused and journaled", 1, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec.entries = nil
			if _, err := db.Exec(`DELETE FROM skills`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO skills (slug, locked) VALUES (?, ?)`, slug, tc.locked); err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			paths := c.promoteProposalSkills(context.Background(), Config{WorkspaceID: "ws"}, rules, scores, "", out, time.Now())
			if len(paths) != tc.wantStaged {
				t.Fatalf("staged %d, want %d", len(paths), tc.wantStaged)
			}
			files, _ := filepath.Glob(filepath.Join(out, ".proposed", "skill-*"))
			if tc.wantStaged == 0 && len(files) != 0 {
				t.Fatalf("locked slug left files: %v", files)
			}
			if len(rec.entries) != tc.wantBlock || (tc.wantBlock == 1 && rec.entries[0].Type != journal.EntrySkillWriteBlocked) {
				t.Fatalf("journal entries = %+v", rec.entries)
			}
		})
	}
}
