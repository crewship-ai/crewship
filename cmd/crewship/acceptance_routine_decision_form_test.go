package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Cross the real HTTP router, auth, waitpoint store and parked-run resumption.
// Only wait and transform steps are used; no agent or external service runs.
func TestAcceptanceRoutineTypedDecisionHTTP(t *testing.T) {
	path, _ := startRoutineTriggerAcceptanceServer(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct{ Server, Workspace, Token string }
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/workspaces/" + config.Workspace + "/pipelines/"
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, endpoint, body string, authenticated bool, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, config.Server+endpoint, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+config.Token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Prefer", "respond-async")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, endpoint, res.StatusCode, want, data)
		}
		return data
	}
	decode := func(raw []byte) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	definition := `{"name":"human-decision-http","steps":[{"id":"gate","type":"wait","wait":{"kind":"approval","approval_prompt":"Choose next","decision_form":{"fields":[{"name":"count","type":"integer","required":true},{"name":"enabled","type":"boolean","required":true}],"actions":[{"id":"go","label":"Continue","approved":true},{"id":"stop","label":"Stop","approved":false}]}}},{"id":"result","type":"transform","needs":["gate"],"transform":{"input":"{{ steps.gate.output }}","expression":".data"}}]}`
	draft := decode(request("POST", base+"drafts", `{"slug":"human-decision-http","document":{"slug":"human-decision-http","definition":`+definition+`}}`, true, 200))
	proof := decode(request("POST", base+"test_run", `{"definition":`+definition+`}`, true, 200))
	publication, err := json.Marshal(map[string]any{"id": draft["id"], "revision": draft["revision"], "save_token": proof["save_token"], "approve_risk": true})
	if err != nil {
		t.Fatal(err)
	}
	request("POST", base+"human-decision-http/publish", string(publication), true, 201)
	accepted := decode(request("POST", base+"human-decision-http/run", `{}`, true, 202))
	runID, ok := accepted["run_id"].(string)
	if !ok || runID == "" {
		t.Fatalf("missing accepted run: %v", accepted)
	}
	var token string
	deadline := time.Now().Add(10 * time.Second)
	for token == "" && time.Now().Before(deadline) {
		var rows []struct {
			Token    string          `json:"token"`
			RunID    string          `json:"pipeline_run_id"`
			Form     json.RawMessage `json:"decision_form"`
			Callback string          `json:"callback_url"`
		}
		if err := json.Unmarshal(request("GET", base+"waitpoints", "", true, 200), &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.RunID == runID {
				if len(row.Form) == 0 || row.Callback != "" {
					t.Fatalf("typed decision audience/projection is wrong: %+v", row)
				}
				token = row.Token
			}
		}
		if token == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if token == "" {
		t.Fatal("run did not create its decision")
	}
	request("POST", "/api/v1/waitpoint-tokens/"+token, `{"approved":true,"payload":{"action_id":"go","data":{"count":0,"enabled":false}}}`, false, 403)
	request("POST", base+"waitpoints/"+token+"/approve", `{"approved":true,"action_id":"go","data":{"count":"0","enabled":false}}`, true, 400)
	request("POST", base+"waitpoints/"+token+"/approve", `{"approved":true,"action_id":"go","data":{"count":0,"enabled":false}}`, true, 200)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run := decode(request("GET", "/api/v1/workspaces/"+config.Workspace+"/pipeline-runs/"+runID, "", true, 200))
		if run["status"] == "completed" {
			outputs, ok := run["step_outputs"].(map[string]any)
			if !ok {
				t.Fatalf("outputs unavailable: %v", run)
			}
			output, ok := outputs["gate"].(string)
			if !ok {
				t.Fatalf("decision output unavailable: %v", outputs)
			}
			answer := decode([]byte(output))
			data, ok := answer["data"].(map[string]any)
			if !ok || answer["action_id"] != "go" || data["count"] != float64(0) || data["enabled"] != false {
				t.Fatalf("typed answer changed: %v", answer)
			}
			return
		}
		if run["status"] == "failed" {
			t.Fatalf("approved run failed: %v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("approved run did not resume")
}
