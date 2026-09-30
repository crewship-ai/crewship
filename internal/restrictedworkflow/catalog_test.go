//go:build linux

package restrictedworkflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestCatalogFiltersUnadmittedGraphsAndPrivateDefinitionFields(t *testing.T) {
	s, _ := graphFixture(t)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('simple','w','simple','Allowed',?,?,'crew','active')`, routine, hash(routine)); err != nil {
		t.Fatal(err)
	}
	store := access.Store{DB: s.db}
	member, err := store.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", member, []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}}); err != nil {
		t.Fatal(err)
	}
	list, err := s.Catalog(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Slug != "simple" || len(list[0].Inputs) != 1 || list[0].Inputs[0].Name != "task" {
		t.Fatal("foreign graph metadata leaked or allowed catalog missing", list)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"agent_run", "Continue", "agent_slug", "other-key", "definition_json"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("catalog exposed recipe internals", secret)
		}
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE workspace_members SET capabilities='[]' WHERE user_id='h1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Catalog(t.Context(), "h1", "w"); err == nil {
		t.Fatal("routine capability revoked but directory available")
	}
}

func TestCatalogFingerprintRejectsChangedNestedRecipeBeforeCreatingScope(t *testing.T) {
	s, _ := graphFixture(t)
	items, err := s.Catalog(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	var selected CatalogRoutine
	for _, item := range items {
		if item.Slug == "private-work" {
			selected = item
		}
	}
	if selected.ExecutionHash == "" {
		t.Fatal("missing compiled graph fingerprint")
	}
	changed := strings.Replace(nestedRecipe, "Review ", "Changed ", 1)
	if _, err = s.db.ExecContext(t.Context(), `UPDATE pipelines SET definition_json=?,definition_hash=? WHERE id='nested'`, changed, hash(changed)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitManualFrozen(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, selected.DefinitionHash, selected.ExecutionHash, 0, "same-request-id"); err == nil {
		t.Fatal("stale nested catalog version admitted")
	}
	for _, table := range []string{"chats", "access_attempts", "restricted_workflow_jobs"} {
		var n int
		if err = s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatal("stale version created execution scope", table, n, err)
		}
	}
}
