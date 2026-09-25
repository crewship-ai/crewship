package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/pages"
)

// Validate real Python output with the exact Page schemas used by the server.
func TestBusinessScriptPayloads(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	for _, name := range []string{"story.py", "catalogue.json"} {
		b, e := seeddata.StoryFileContent("stories/business/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	bindings := map[string]map[string]string{}
	for _, s := range seeddata.Stories {
		bindings[s.Slug] = map[string]string{"identifier": "DEMO-1"}
	}
	b, _ := json.Marshal(bindings)
	if err = os.WriteFile(filepath.Join(root, "bindings.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, s := range seeddata.Stories {
		for _, action := range []string{"check", "prepare", "complete", "complete"} {
			cmd := exec.Command(python, "-B", filepath.Join(root, "story.py"), s.Slug, action)
			cmd.Env = append(os.Environ(), "CREWSHIP_DEMO_ROOT="+root, "NO_PROXY=127.0.0.1")
			raw, e := cmd.CombinedOutput()
			if e != nil {
				t.Fatalf("%s %s: %v: %s", s.Slug, action, e, raw)
			}
			var output map[string]json.RawMessage
			if e = json.Unmarshal(raw, &output); e != nil {
				t.Fatal(e)
			}
			for field, schema := range map[string]pages.PanelSchema{"records": pages.SchemaTable, "summary": pages.SchemaNarrative} {
				if _, e = pages.ValidatePayload(schema, output[field]); e != nil {
					t.Fatalf("%s %s %s: %v", s.Slug, action, field, e)
				}
			}
		}
	}
}
