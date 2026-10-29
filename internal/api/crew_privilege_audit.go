package api

import (
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// Record every runtime escape hatch the save path honours (privileged, added
// capabilities, init, host/volume mounts), never the raw runtime config which
// may also contain private environments, commands or registry addresses.
// remoteUser is not recorded: Validate admits only the fixed agent user.
func crewPrivilegeAudit(raw string) map[string]any {
	facts := map[string]any{"privileged": false, "cap_add": []string{}, "init": false, "mounts": []devcontainer.FeatureMount{}}
	if strings.TrimSpace(raw) == "" {
		return facts
	}
	cfg, err := devcontainer.ParseBytes([]byte(raw))
	if err != nil {
		return map[string]any{"stored_config_invalid": true}
	}
	capabilities := make([]string, 0, len(cfg.CapAdd))
	for _, capability := range cfg.CapAdd {
		capabilities = append(capabilities, devcontainer.NormalizeCapability(capability))
	}
	sort.Strings(capabilities)
	mounts := append([]devcontainer.FeatureMount{}, cfg.Mounts...)
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].Target != mounts[j].Target {
			return mounts[i].Target < mounts[j].Target
		}
		return mounts[i].Source < mounts[j].Source
	})
	facts["privileged"], facts["cap_add"], facts["init"], facts["mounts"] = cfg.Privileged, capabilities, cfg.Init, mounts
	return facts
}
