package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestPresetCLIApplyCommandExists(t *testing.T) {
	cmd, remaining, err := rootCmd.Find([]string{"workspace", "member", "access", "preset", "apply"})
	if err != nil || cmd == nil || cmd.Name() != "apply" || len(remaining) != 0 {
		t.Fatalf("explicit preset apply command missing: command=%v remaining=%v err=%v", cmd, remaining, err)
	}
}

func TestPresetCLIApplyExactConsentAndNoRetry(t *testing.T) {
	guardCLIState(t)
	for _, status := range []int{200, 409, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			stub := covStub(t)
			flagFormat = "json"
			stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
				return 200, []byte(memberIDRoster), "application/json"
			})
			path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access/presets/apply"
			input := `{"selection":` + presetCLISelection + `,"confirmation_digest":"` + strings.Repeat("a", 64) + `"}`
			stub.OnPost(path, func(r *http.Request, b []byte) (int, []byte, string) {
				var sent confirmedPreset
				if err := json.Unmarshal(b, &sent); err != nil || sent.Selection.Revision != 7 || sent.Selection.Level != "manage" || sent.Digest != strings.Repeat("a", 64) || len(sent.Selection.Reissue) != 0 {
					t.Fatalf("changed explicit consent:%s %v", b, err)
				}
				if status != 200 {
					return status, []byte(`{"error":"review a new preview"}`), "application/json"
				}
				return 200, []byte(`{"membership_id":"m-viewer","revision":9,"mode":"restricted","rights":[],"preset_selections":[]}`), "application/json"
			})
			cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
			cmd.SetIn(strings.NewReader(input))
			var err error
			out := covCaptureAll(t, func() { err = workspaceAccessPresetApplyCmd.RunE(cmd, []string{"u-viewer"}) })
			if status == 200 {
				if err != nil || !strings.Contains(out, `"revision": 9`) {
					t.Fatalf("apply output:%s %v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Fatalf("hidden conflict/denial:%v", err)
			}
			if len(stub.CallsFor("POST", path)) != 1 || len(stub.Calls()) != 2 {
				t.Fatalf("silently refreshed/retried consent:%+v", stub.Calls())
			}
		})
	}
}
func TestPresetCLIApplyRejectsInvalidConsentBeforeNetwork(t *testing.T) {
	valid := `{"selection":` + presetCLISelection + `,"confirmation_digest":"` + strings.Repeat("a", 64) + `"}`
	for _, body := range []string{valid + ` {}`, strings.TrimSuffix(valid, "}") + `,"actor":"forged"}`, strings.Replace(valid, `"level":"manage"`, `"level":"manage","issuer":"forged"`, 1), strings.Replace(valid, strings.Repeat("a", 64), "bad", 1), valid + strings.Repeat(" ", 256<<10)} {
		stub := covStub(t)
		cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
		cmd.SetIn(strings.NewReader(body))
		if err := workspaceAccessPresetApplyCmd.RunE(cmd, []string{"u-viewer"}); err == nil {
			t.Fatal("invalid consent accepted")
		}
		if len(stub.Calls()) != 0 {
			t.Fatal("invalid consent reached network")
		}
	}
}
