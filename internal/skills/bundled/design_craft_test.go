package bundled_test

import (
	"context"
	"io/fs"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
	"github.com/crewship-ai/crewship/internal/skills/bundled"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Exercise the installed catalog, not just the existence of the source files:
// an embedded skill without registration is unavailable to agent assignments.
func TestDesignCraftSkills(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	if err := bundled.Install(context.Background(), db, slog.New(slog.NewTextHandler(discardWriter{}, nil))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ slug, category, icon string }{
		{"crewship-design", "DESIGN", "layers"},
		{"crewship-craft", "WRITING", "pen"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			body, err := fs.ReadFile(bundled.FS(), "crewship/"+tc.slug+"/SKILL.md")
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := skills.ParseSKILLMD(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Meta.Name != tc.slug || parsed.Meta.Category != tc.category || parsed.Meta.Description == "" {
				t.Fatalf("invalid metadata: %+v", parsed.Meta)
			}
			var category, icon, content string
			if err := db.QueryRow(`SELECT category,icon,content FROM skills WHERE vendor='crewship' AND slug=?`, tc.slug).Scan(&category, &icon, &content); err != nil {
				t.Fatal(err)
			}
			if category != tc.category || icon != tc.icon || content != parsed.Content {
				t.Fatalf("installed skill differs: category=%q icon=%q", category, icon)
			}
		})
	}
}
