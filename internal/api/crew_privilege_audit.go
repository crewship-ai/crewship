package api

import (
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// Record explicit privilege/capability values, never the raw runtime config
// which may also contain private environments, commands or registry addresses.
func crewPrivilegeAudit(raw string) map[string]any {
	facts := map[string]any{"privileged": false, "cap_add": []string{}}
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
	facts["privileged"], facts["cap_add"] = cfg.Privileged, capabilities
	return facts
}
