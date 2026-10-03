package devcontainer

import (
	"encoding/json"
	"strings"
	"testing"
)

func applyPlanFixture() (*MiseConfig, *MiseResolution) {
	cfg := &MiseConfig{Tools: map[string]string{"node": "22.0.0"}, Env: map[string]string{"CUSTOM": "preserved"}, AICLICheck: "required"}
	lock := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "lockfile_version = 3\n", ".mise/locks/node/package.json": "{}"}}
	return cfg, &MiseResolution{SelectorsSHA256: miseSelectorsDigest(cfg.Tools), LockSHA256: miseLockDigest(lock), LockChanged: true, Lock: lock, ImageID: "sha256:" + strings.Repeat("a", 64), Platform: "linux-x64", MiseVersion: "2026.9.18"}
}

func TestApplyMiseResolutionBindsCurrentInputsAndCompleteOutput(t *testing.T) {
	for _, mode := range []string{"valid", "changed selectors", "changed old lock", "corrupt lock", "changed auxiliary", "missing lock", "invalid bundle", "inconsistent changed flag", "missing image", "unsupported platform"} {
		t.Run(mode, func(t *testing.T) {
			cfg, plan := applyPlanFixture()
			switch mode {
			case "changed selectors":
				cfg.Tools["node"] = "latest"
			case "changed old lock":
				cfg.Lock = &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "old"}}
			case "corrupt lock":
				plan.Lock.Files["mise.lock"] += "changed"
			case "changed auxiliary":
				plan.Lock.Files[".mise/locks/node/package.json"] = `{"changed":true}`
			case "missing lock":
				plan.Lock = nil
			case "invalid bundle":
				plan.Lock.Files["../mise.lock"] = "unsafe"
				plan.LockSHA256 = miseLockDigest(plan.Lock)
			case "inconsistent changed flag":
				plan.LockChanged = false
			case "missing image":
				plan.ImageID = ""
			case "unsupported platform":
				plan.Platform = "unknown"
			}
			got, err := ApplyMiseResolution(cfg, plan)
			if mode != "valid" {
				if err == nil || got != nil {
					t.Fatal("invalid/stale proposal emitted a config", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Tools["node"] != "22.0.0" || got.Env["CUSTOM"] != "preserved" || got.AICLICheck != "required" || got.Lock.Files["mise.lock"] != plan.Lock.Files["mise.lock"] {
				t.Fatalf("inputs changed: %+v", got)
			}
			got.Tools["node"] = "changed"
			got.Env["CUSTOM"] = "changed"
			got.Lock.Files["mise.lock"] = "changed"
			if cfg.Tools["node"] != "22.0.0" || cfg.Env["CUSTOM"] != "preserved" || plan.Lock.Files["mise.lock"] == "changed" {
				t.Fatal("result aliases caller inputs")
			}
		})
	}
}

func TestApplyMiseResolutionPreservesCurrentNonResolverFields(t *testing.T) {
	cfg, plan := applyPlanFixture()
	raw := `{"tools":{"node":"22.0.0"},"env":{"CUSTOM":"changed since plan"},"ai_cli_check":"required","tasks":{"build":"echo build"}}`
	got, err := ApplyMiseResolutionInput(raw, plan)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	var tasks map[string]string
	if err := json.Unmarshal(fields["tasks"], &tasks); err != nil {
		t.Fatal(err)
	}
	if tasks["build"] != "echo build" || !strings.Contains(string(fields["env"]), "changed since plan") || string(fields["ai_cli_check"]) != `"required"` {
		t.Fatalf("non-resolver inputs were lost: %s", got)
	}
	if cfg.Lock != nil {
		t.Fatal("source changed")
	}
	if _, err := ApplyMiseResolutionInput(`[tools]
node = "22.0.0"`, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyMiseResolutionInput(`{"tools":{"node":"22.0.0"},"unknown":"`+strings.Repeat("x", 11<<10)+`"}`, plan); err == nil {
		t.Fatal("oversized non-lock input accepted")
	}
}

func TestApplyMiseResolutionNoChangeAndStaleReuse(t *testing.T) {
	cfg, plan := applyPlanFixture()
	updated, err := ApplyMiseResolution(cfg, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyMiseResolution(updated, plan); err == nil {
		t.Fatal("old proposal applied twice despite changed previous lock")
	}
	plan.PreviousLockSHA256 = plan.LockSHA256
	plan.LockChanged = false
	if _, err := ApplyMiseResolution(updated, plan); err != nil {
		t.Fatal("valid no-change proposal rejected", err)
	}
}
