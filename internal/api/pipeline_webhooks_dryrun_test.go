package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ODCH-INT-09: "Test mapping" answers what a sample call would hand the
// routine, without running it and without the signing secret.
func TestDryRunWebhook(t *testing.T) {
	definition := `{"name":"webhook-form","agentless":true,"inputs":[{"name":"qty","type":"integer","required":true,"widget":"number","min":1}],"steps":[{"id":"echo","type":"transform","transform":{"input":"{{ inputs.qty }}","expression":"."}}]}`
	tests := []struct {
		name         string
		template     string
		body         string
		role         string
		wantCode     int
		wantValid    bool
		wantProblem  string
		wantInputs   map[string]any
		wantReserved []string
	}{
		{
			name: "a good mapping is valid and shows the inputs", template: `{"qty":9,"source":"gh"}`,
			body: `{"sample":{"action":"opened"}}`, role: "OWNER", wantCode: 200, wantValid: true,
			wantInputs: map[string]any{"qty": float64(9), "source": "gh"},
		},
		{
			name: "a missing required input is reported, nothing runs", template: `{}`,
			body: `{"sample":{"action":"opened"}}`, role: "OWNER", wantCode: 200, wantValid: false, wantProblem: "qty",
		},
		{
			name: "a template key that tries to override the request is dropped and named", template: `{"qty":2,"event":"tampered"}`,
			body: `{"sample":{"action":"opened"}}`, role: "MANAGER", wantCode: 200, wantValid: true,
			wantInputs: map[string]any{"qty": float64(2)}, wantReserved: []string{"event"},
		},
		{
			name: "a viewer may not use it", template: `{"qty":1}`,
			body: `{"sample":{}}`, role: "VIEWER", wantCode: 403,
		},
		{
			name: "a body that is not JSON is refused", template: `{"qty":1}`,
			body: `nope`, role: "OWNER", wantCode: 400,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := webhookHandlerRig(t)
			seedWebhookPipeline(t, db, wsID, "pln_dry", "dry-run")
			if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='pln_dry'`, definition); err != nil {
				t.Fatal(err)
			}
			wh := seedWebhookRow(t, db, wsID, "pln_dry", "fixture-signing-key", true)
			if _, err := db.Exec(`UPDATE pipeline_webhooks SET inputs_template=? WHERE id=?`, tc.template, wh.ID); err != nil {
				t.Fatal(err)
			}
			req := withWorkspaceUser(httptest.NewRequest("POST",
				"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID+"/test", strings.NewReader(tc.body)),
				userID, wsID, tc.role)
			req.SetPathValue("webhookId", wh.ID)
			rr := httptest.NewRecorder()
			h.DryRunWebhook(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			var out webhookDryRunResponse
			decodeJSON(t, rr, &out)
			if out.Valid != tc.wantValid {
				t.Errorf("valid = %v, want %v (problem %q)", out.Valid, tc.wantValid, out.Problem)
			}
			if tc.wantProblem != "" && !strings.Contains(out.Problem, tc.wantProblem) {
				t.Errorf("problem = %q, want it to name %q", out.Problem, tc.wantProblem)
			}
			for k, want := range tc.wantInputs {
				if out.Inputs[k] != want {
					t.Errorf("inputs[%s] = %v, want %v", k, out.Inputs[k], want)
				}
			}
			if ev, ok := out.Inputs["event"].(map[string]any); !ok || ev["action"] != "opened" && tc.wantProblem == "" {
				t.Errorf("inputs.event = %v, want the parsed sample", out.Inputs["event"])
			}
			if strings.Join(out.IgnoredTemplateKeys, ",") != strings.Join(tc.wantReserved, ",") {
				t.Errorf("ignored_template_keys = %v, want %v", out.IgnoredTemplateKeys, tc.wantReserved)
			}
			if strings.Contains(rr.Body.String(), "fixture-signing-key") {
				t.Error("the response leaks the signing secret")
			}
			var runs int
			_ = db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&runs)
			if runs != 0 {
				t.Errorf("a dry run started %d run(s)", runs)
			}
		})
	}
}
