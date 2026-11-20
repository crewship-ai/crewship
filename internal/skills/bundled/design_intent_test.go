package bundled_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills/bundled"
)

func TestDesignSkillRequiresExplicitStyleIntent(t *testing.T) {
	for _, slug := range []string{"crewship-artifacts", "crewship-design"} {
		data, err := fs.ReadFile(bundled.FS(), "crewship/"+slug+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range []string{"unless asked to change the look", "only when asked to hide or lock", "only when asked for corners or a border"} {
			if !strings.Contains(string(data), rule) {
				t.Errorf("%s skill lacks %q", slug, rule)
			}
		}
	}
}
