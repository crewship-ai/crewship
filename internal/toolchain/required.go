package toolchain

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
)

// RequirePassing validates image-bound offline evidence at publication. It
// does not establish provider authentication or constrain later runtime drift.
func RequirePassing(imageID string, inventory *devcontainer.ToolchainInventory, binaries []string) error {
	rejected := errors.New("AI CLI startup check required: missing or failed image-bound offline evidence")
	if !dockerutil.IsLocalImageID(imageID) || inventory == nil || inventory.SchemaVersion != 1 || inventory.Status != "recorded" || inventory.ImageID != imageID || len(inventory.Tools) == 0 || inventory.Qualification == nil {
		return rejected
	}
	q := inventory.Qualification
	if q.Status != "passed" || q.ImageID != imageID || len(q.Tools) != len(inventory.Tools) {
		return rejected
	}
	observed := make(map[string]bool, len(inventory.Tools))
	for _, t := range inventory.Tools {
		if t.Binary == "" || observed[t.Binary] || t.Status != "observed" || t.Version == "" || !path.IsAbs(t.Path) {
			return rejected
		}
		observed[t.Binary] = true
	}
	passed := make(map[string]bool, len(q.Tools))
	for _, t := range q.Tools {
		if !observed[t.Binary] || passed[t.Binary] || t.Status != "passed" {
			return rejected
		}
		passed[t.Binary] = true
	}
	for _, binary := range binaries {
		if !passed[binary] {
			return rejected
		}
	}
	return nil
}

// RequireExactPins checks explicit CLI pins against the image observations.
// Floating selectors retain native mise semantics; this does not parse the
// native lock or independently qualify provider/model compatibility.
func RequireExactPins(inventory *devcontainer.ToolchainInventory, requests []devcontainer.ToolchainRequest) error {
	for _, request := range requests {
		if request.Source != "mise" || !request.Exact {
			continue
		}
		observed := ""
		if inventory != nil {
			for _, tool := range inventory.Tools {
				if tool.Binary == request.Binary && tool.Status == "observed" {
					observed = tool.Version
					break
				}
			}
		}
		if observed != strings.TrimPrefix(request.Selector, "v") {
			return fmt.Errorf("AI CLI startup check required: %s does not match requested version %s", request.Binary, request.Selector)
		}
	}
	return nil
}
