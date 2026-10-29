package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestCrewPresetCLIUsesExistingExplicitConsentFlow(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			stub := covStub(t)
			old := flagFormat
			flagFormat = format
			t.Cleanup(func() { flagFormat = old })
			stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
				return 200, []byte(memberIDRoster), "application/json"
			})
			selection := strings.ReplaceAll(strings.ReplaceAll(presetCLISelection, "project", "crew"), "manage", "use")
			path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access/presets/preview"
			stub.OnPost(path, func(_ *http.Request, body []byte) (int, []byte, string) {
				var input access.PresetRequest
				if err := json.Unmarshal(body, &input); err != nil || input.Resource.Kind != "crew" || input.PresetID != "crew-levels" || input.Level != "use" || input.Revision != 7 {
					t.Fatalf("consent rewritten: %+v %v", input, err)
				}
				return 200, []byte(`{"selection":` + selection + `,"definition_hash":"immutable-crew-v1","confirmation_digest":"confirmed","warnings":[{"code":"CREW_DYNAMIC_AUDIENCE","message":"Shares current and future agents; per-agent preflight remains required."}]}`), "application/json"
			})
			cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
			cmd.SetIn(strings.NewReader(selection))
			out := covCaptureAll(t, func() {
				if err := workspaceAccessPresetPreviewCmd.RunE(cmd, []string{"u-viewer"}); err != nil {
					t.Error(err)
				}
			})
			if !strings.Contains(out, "current and future agents") || !strings.Contains(out, "crew") {
				t.Fatalf("crew consequences missing: %s", out)
			}
			if len(stub.CallsFor("POST", path)) != 1 {
				t.Fatal("preview retried")
			}
		})
	}
}
