package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestRunDefinition_UsesArchivedContentAndNeverHead(t *testing.T) {
	h, db, _, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "definition_pipeline", "historical")
	store := pipeline.NewStore(db)
	old, err := store.SaveVersion(context.Background(), pipeline.SaveVersionInput{PipelineID: "definition_pipeline", DefinitionJSON: `{"steps":[{"id":"old"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveVersion(context.Background(), pipeline.SaveVersionInput{PipelineID: "definition_pipeline", DefinitionJSON: `{"steps":[{"id":"new"}]}`}); err != nil {
		t.Fatal(err)
	}
	seedRunRow(t, db, ws, "definition_pipeline", "historical", "historic_run", "completed")
	if _, err := db.Exec(`UPDATE pipeline_runs SET definition_hash = ? WHERE id = 'historic_run'`, old.DefinitionHash); err != nil {
		t.Fatal(err)
	}
	resp := map[string]interface{}{}
	h.enrichRunDefinition(context.Background(), ws, "historic_run", resp)
	if string(resp["definition"].(json.RawMessage)) != old.DefinitionJSON {
		t.Fatalf("wrong definition: %#v", resp)
	}
	if _, err := db.Exec(`DELETE FROM pipeline_versions WHERE id = ?`, old.ID); err != nil {
		t.Fatal(err)
	}
	resp = map[string]interface{}{}
	h.enrichRunDefinition(context.Background(), ws, "historic_run", resp)
	if resp["definition"] != nil || resp["definition_status"] != "unavailable" {
		t.Fatalf("fell back to HEAD: %#v", resp)
	}
	// Runtime prompt/model overrides remain available even if the archived
	// authored version was removed.
	effective := `{"steps":[{"id":"old","prompt":"effective runtime prompt"}]}`
	if _, err := db.Exec(`UPDATE pipeline_runs SET executed_definition_json=? WHERE id='historic_run'`, effective); err != nil {
		t.Fatal(err)
	}
	resp = map[string]interface{}{}
	h.enrichRunDefinition(context.Background(), ws, "historic_run", resp)
	if string(resp["definition"].(json.RawMessage)) != effective {
		t.Fatalf("lost effective snapshot: %#v", resp)
	}
	resp = map[string]interface{}{}
	h.enrichRunDefinition(context.Background(), "foreign", "historic_run", resp)
	if resp["definition"] != nil {
		t.Fatal("foreign workspace leaked definition")
	}
}
