package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestDraftPublishAcceptsBrowserSerializedDefinition(t *testing.T) {
	path, _ := startRoutineTriggerAcceptanceServer(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct{ Server, Workspace, Token string }
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	post := func(endpoint, body string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, config.Server+"/api/v1/workspaces/"+config.Workspace+"/pipelines/"+endpoint, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+config.Token)
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		out, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s: status %d, want %d: %s", endpoint, response.StatusCode, want, out)
		}
		return out
	}
	// Literal JSON.stringify-style bytes cross the real router and HTTP stack.
	// The test gate must not invoke this HTTP step or contact any external system.
	definition := `{"name":"browser-proof","description":"Sales & Ops <review>","steps":[{"id":"fetch","type":"http","if":"1 > 0","http":{"method":"GET","url":"https://example.test/?a=1&b=2"}}]}`
	saved := post("drafts", `{"slug":"browser-proof","document":{"slug":"browser-proof","name":"Sales & Ops","definition":`+definition+`}}`, http.StatusOK)
	var draft pipeline.Draft
	if err := json.Unmarshal(saved, &draft); err != nil {
		t.Fatal(err)
	}
	checked := post("test_run", `{"definition":`+definition+`}`, http.StatusOK)
	var proof struct {
		SaveToken string `json:"save_token"`
	}
	if err := json.Unmarshal(checked, &proof); err != nil {
		t.Fatal(err)
	}
	if proof.SaveToken == "" {
		t.Fatal("test_run did not mint a proof")
	}
	body, err := json.Marshal(map[string]any{"id": draft.ID, "revision": draft.Revision, "save_token": proof.SaveToken, "approve_risk": true})
	if err != nil {
		t.Fatal(err)
	}
	post("browser-proof/publish", string(body), http.StatusCreated)
}
