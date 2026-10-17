package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

const presetCLISelection = `{"membership_id":"m-viewer","revision":7,"resource":{"kind":"project","id":"project"},"preset_id":"project-levels","preset_version":1,"level":"manage","reissue":[]}`

func TestPresetCLIPreviewExplicitRevisionAndNoRetry(t *testing.T) {
	for _, status := range []int{200, 409, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub := covStub(t)
			stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
				return 200, []byte(memberIDRoster), "application/json"
			})
			path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access/presets/preview"
			stub.OnPost(path, func(r *http.Request, b []byte) (int, []byte, string) {
				var req access.PresetRequest
				if err := json.Unmarshal(b, &req); err != nil || req.MembershipID != "m-viewer" || req.Revision != 7 || req.Level != "manage" || len(req.Reissue) != 0 {
					t.Fatalf("rewrote preview consent: %+v %v", req, err)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatal("changed credential")
				}
				if status != 200 {
					return status, []byte(`{"error":"current policy unavailable"}`), "application/json"
				}
				return 200, []byte(`{"selection":` + presetCLISelection + `,"confirmation_digest":"digest","warnings":[{"code":"RETAINED_DELETE_ISSUES","message":"Conditional destructive right","before_eligible":false,"after_eligible":true}],"changes":[{"right":{"kind":"project","id":"project","operation":"read"},"action":"retain","issuer_id":"original-issuer","created_at":"original","live":false}]}`), "application/json"
			})
			cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
			cmd.SetIn(strings.NewReader(presetCLISelection))
			var err error
			out := covCaptureAll(t, func() { err = workspaceAccessPresetPreviewCmd.RunE(cmd, []string{"u-viewer"}) })
			if status == 200 {
				if err != nil || !strings.Contains(out, "false → true") || !strings.Contains(out, "original-issuer") || !strings.Contains(out, "original") {
					t.Fatalf("missing consequences/provenance: %s %v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Fatalf("suppressed server error:%v", err)
			}
			if len(stub.CallsFor("POST", path)) != 1 || len(stub.CallsFor("GET", strings.TrimSuffix(path, "/presets/preview"))) != 0 {
				t.Fatal("silently refreshed/retried preview")
			}
		})
	}
}

func TestPresetCLIRejectsForgedTrailingOversizedInputBeforeNetwork(t *testing.T) {
	for _, body := range []string{presetCLISelection + ` {}`, strings.TrimSuffix(presetCLISelection, "}") + `,"actor":"forged"}`, strings.Replace(presetCLISelection, `"resource":{`, `"resource":{"issuer":"forged",`, 1), presetCLISelection + strings.Repeat(" ", 256<<10)} {
		stub := covStub(t)
		cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
		cmd.SetIn(strings.NewReader(body))
		if err := workspaceAccessPresetPreviewCmd.RunE(cmd, []string{"u-viewer"}); err == nil {
			t.Fatal("invalid selection accepted")
		}
		if len(stub.Calls()) != 0 {
			t.Fatal("invalid input reached network")
		}
	}
}

func TestPresetCLIPreviewFileStructuredOutput(t *testing.T) {
	stub := covStub(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access/presets/preview"
	stub.OnPost(path, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"selection":` + presetCLISelection + `,"confirmation_digest":"exact-digest"}`), "application/json"
	})
	file := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(file, []byte(presetCLISelection), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newFlagCmd(map[string]string{"file": file}, nil)
	out := covCaptureAll(t, func() {
		if err := workspaceAccessPresetPreviewCmd.RunE(cmd, []string{"u-viewer"}); err != nil {
			t.Error(err)
		}
	})
	var preview access.PresetPreview
	if err := json.Unmarshal([]byte(out), &preview); err != nil || preview.Digest != "exact-digest" || preview.Selection.Revision != 7 {
		t.Fatalf("not structured consent:%s %v", out, err)
	}

}

func TestPresetCLIPreviewYAMLPreservesConsentAndLargeRevision(t *testing.T) {
	stub := covStub(t)
	flagFormat = "yaml"
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	selection := strings.Replace(presetCLISelection, `"revision":7`, `"revision":9007199254740993`, 1)
	path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access/presets/preview"
	stub.OnPost(path, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"selection":` + selection + `,"confirmation_digest":"checksum","current_selection":null,"before":[],"changes":[],"after_rights":[],"orthogonal_rights":[],"warnings":[]}`), "application/json"
	})
	cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
	cmd.SetIn(strings.NewReader(selection))
	out := covCaptureAll(t, func() {
		if err := workspaceAccessPresetPreviewCmd.RunE(cmd, []string{"u-viewer"}); err != nil {
			t.Error(err)
		}
	})
	for _, expected := range []string{"membership_id: m-viewer", "revision: 9007199254740993", "confirmation_digest: checksum", "orthogonal_rights: []"} {
		if !strings.Contains(out, expected) {
			t.Fatalf("YAML lost consent field %s: %s", expected, out)
		}
	}
}
