package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
	"github.com/crewship-ai/crewship/internal/skills/bundled"
)

func TestInstalledSkillPreservesReleaseAndEscapesYAML(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "package-agent", wsID, "", "Package", "package-agent", "AGENT")
	covCfgSeedSkillFull(t, db, "package-skill", "package-skill", "crewship", "Package", "Author artifacts", "", "## Dashboard\nUse artifact_data_batch.")
	covCfgAssignSkill(t, db, "package-assignment", agentID, "package-skill", 1)
	h := covCfgHandler(db)
	for _, version := range []string{"1.1.0", "1.1.0\"\nvendor: attacker\nname: replaced", "release: 'quoted' \\ value", "nul\x00tab\treturn\rnewline\n", "release\u007Fsuffix", "release\u0080suffix", "release\u0085suffix", "release\u009Fsuffix", "release\u2028suffix", "release\u2029suffix"} {
		t.Run(fmt.Sprintf("%q", version), func(t *testing.T) {
			if _, err := db.Exec(`UPDATE skills SET version=? WHERE id='package-skill'`, version); err != nil {
				t.Fatal(err)
			}
			out, err := h.resolveInstalledSkills(httptest.NewRequest("GET", "/", nil), agentID)
			if err != nil || len(out) != 1 {
				t.Fatalf("resolve: %v %+v", err, out)
			}
			parsed, err := skills.ParseSKILLMD(out[0].Content)
			if err != nil || parsed.Meta.Version != version || parsed.Meta.Name != "package-skill" || parsed.Meta.Vendor != "crewship" {
				t.Fatalf("release lost or YAML injected: %+v %v", parsed, err)
			}
		})
	}
}

func TestInstalledArtifactSkillUsesBundledReleaseAndInstructions(t *testing.T) {
	db := setupTestDB(t)
	if err := bundled.Install(t.Context(), db, newTestLogger()); err != nil {
		t.Fatal(err)
	}
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "artifact-package-agent", wsID, "", "Package", "artifact-package-agent", "AGENT")
	var skillID string
	if err := db.QueryRow(`SELECT id FROM skills WHERE vendor='crewship' AND slug='crewship-artifacts'`).Scan(&skillID); err != nil {
		t.Fatal(err)
	}
	covCfgAssignSkill(t, db, "artifact-package-assignment", agentID, skillID, 1)
	out, err := covCfgHandler(db).resolveInstalledSkills(httptest.NewRequest("GET", "/", nil), agentID)
	if err != nil || len(out) != 1 {
		t.Fatalf("resolve: %v %+v", err, out)
	}
	parsed, err := skills.ParseSKILLMD(out[0].Content)
	if err != nil || parsed.Meta.Version != "1.1.0" {
		t.Fatalf("lost stored package version: %+v %v", parsed, err)
	}
	for _, section := range []string{"## Project", "## Dashboard", "## Slides"} {
		if !strings.Contains(parsed.Content, section) {
			t.Fatalf("missing %s", section)
		}
	}
}
