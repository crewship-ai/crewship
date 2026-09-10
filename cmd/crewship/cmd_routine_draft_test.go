package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineDraftPublishValidatesExactRevision(t *testing.T) {
	stub := covSetupCli5(t)
	base := "/api/v1/workspaces/" + covWSCli5 + "/pipelines"
	file := filepath.Join(t.TempDir(), "draft.json")
	raw := `{"id":"d1","slug":"digest","revision":7,"document":{"slug":"digest","author_crew_id":"crew1","definition":{"name":"digest","steps":[]}}}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	validated, published := false, false
	stub.OnPost(base+"/test_run", func(r *http.Request, body []byte) (int, []byte, string) {
		var b map[string]any
		if err := json.Unmarshal(body, &b); err != nil {
			t.Error(err)
			return 400, nil, "application/json"
		}
		if b["author_crew_id"] != "crew1" {
			t.Error("lost author context")
		}
		definition, ok := b["definition"].(map[string]any)
		if !ok || definition["name"] != "digest" {
			t.Error("validated wrong definition")
		}
		validated = true
		return 200, []byte(`{"status":"DRY_RUN_OK","save_token":"proof"}`), "application/json"
	})
	stub.OnPost(base+"/digest/publish", func(r *http.Request, body []byte) (int, []byte, string) {
		if !validated {
			t.Error("published before validation")
		}
		var b map[string]any
		if err := json.Unmarshal(body, &b); err != nil {
			t.Error(err)
			return 400, nil, "application/json"
		}
		if b["id"] != "d1" || b["revision"] != float64(7) || b["save_token"] != "proof" || b["approve_risk"] != false {
			t.Errorf("publication=%v", b)
		}
		published = true
		return 201, []byte(`{"slug":"digest"}`), "application/json"
	})
	cmd := newRoutineDraftCommand("publish")
	if err := cmd.RunE(cmd, []string{file}); err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("never published")
	}
}

func TestRoutineDraftPublishFailsWithoutProof(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/test_run", clitest.JSONResponse(200, map[string]any{"status": "DRY_RUN_OK"}))
	file := filepath.Join(t.TempDir(), "draft.json")
	os.WriteFile(file, []byte(`{"id":"d","slug":"digest","revision":1,"document":{"definition":{"name":"digest"}}}`), 0600)
	cmd := newRoutineDraftCommand("publish")
	if err := cmd.RunE(cmd, []string{file}); err == nil {
		t.Fatal("accepted missing proof")
	}
}

func TestRoutineDraftPublishRejectsMissingDefinitionLocally(t *testing.T) {
	for _, doc := range []string{`{}`, `{"definition":null}`, `{"definition":[]}`} {
		t.Run(doc, func(t *testing.T) {
			stub := covSetupCli5(t)
			called := false
			stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/test_run", func(*http.Request, []byte) (int, []byte, string) { called = true; return 400, nil, "application/json" })
			file := filepath.Join(t.TempDir(), "draft.json")
			if err := os.WriteFile(file, []byte(`{"id":"d","slug":"digest","revision":1,"document":`+doc+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := newRoutineDraftCommand("publish")
			err := cmd.RunE(cmd, []string{file})
			if err == nil || !strings.Contains(err.Error(), "definition object") || called {
				t.Fatalf("expected local definition error without HTTP request: %v, called=%v", err, called)
			}
		})
	}
}
