package toolchain

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestRequirePassingRejectsIncompleteEvidence(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	for _, mode := range []string{"passed", "nil inventory", "schema", "status", "inventory image", "missing version", "missing path", "duplicate inventory", "missing expected binary", "probe failure", "empty evidence"} {
		t.Run(mode, func(t *testing.T) {
			inv := &devcontainer.ToolchainInventory{SchemaVersion: 1, Status: "recorded", ImageID: image, Tools: []devcontainer.ToolchainTool{{Binary: "codex", Path: "/usr/bin/codex", Version: "1.2.3", Status: "observed"}}, Qualification: &devcontainer.ToolchainQualification{ImageID: image, Status: "passed", Tools: []devcontainer.ToolchainProbe{{Binary: "codex", Status: "passed"}}}}
			binaries := []string{"codex"}
			switch mode {
			case "nil inventory":
				inv = nil
			case "schema":
				inv.SchemaVersion = 2
			case "status":
				inv.Status = "unavailable"
			case "inventory image":
				inv.ImageID = "other"
			case "missing version":
				inv.Tools[0].Version = ""
			case "missing path":
				inv.Tools[0].Path = ""
			case "duplicate inventory":
				inv.Tools = append(inv.Tools, inv.Tools[0])
				inv.Qualification.Tools = append(inv.Qualification.Tools, inv.Qualification.Tools[0])
			case "missing expected binary":
				binaries = append(binaries, "claude")
			case "probe failure":
				inv.Qualification.Tools[0].Status = "probe_failed"
			case "empty evidence":
				inv.Tools = nil
				inv.Qualification.Tools = nil
			}
			if err := RequirePassing(image, inv, binaries); (err == nil) != (mode == "passed") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}

func TestRequireExactPinsUsesDeclaredVersions(t *testing.T) {
	for _, tc := range []struct {
		selector, observed, source string
		exact, want                bool
	}{
		{"1.2.3", "1.2.3", "mise", true, true},
		{"v1.2.3", "1.2.3", "mise", true, true},
		{"1.2.3", "1.2.4", "mise", true, false},
		{"1.2.3", "", "mise", true, false},
		{"1.2", "1.2.4", "mise", false, true},
		{"latest", "1.2.4", "mise", false, true},
		{"", "1.2.4", "devcontainer_feature", false, true},
	} {
		inv := &devcontainer.ToolchainInventory{Tools: []devcontainer.ToolchainTool{{Binary: "codex", Version: tc.observed, Status: "observed"}}}
		requests := []devcontainer.ToolchainRequest{{Binary: "codex", Selector: tc.selector, Source: tc.source, Exact: tc.exact}}
		if err := RequireExactPins(inv, requests); (err == nil) != tc.want {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if err := RequireExactPins(nil, []devcontainer.ToolchainRequest{{Binary: "codex", Selector: "1.2.3", Source: "mise", Exact: true}}); err == nil {
		t.Fatal("unknown image evidence satisfied a pin")
	}
}
