package orchestrator

import (
	"github.com/crewship-ai/crewship/internal/credname"
	"github.com/crewship-ai/crewship/internal/credpolicy"
	"sort"
)

// CredentialNameProblem contains metadata, never material or payload errors.
type CredentialNameProblem struct {
	Alias         string   `json:"alias"`
	CredentialIDs []string `json:"credential_ids"`
	Reason        string   `json:"reason"`
}

// CredentialUpgradeMCPReferences shares the runtime's reference extraction.
func CredentialUpgradeMCPReferences(configs ...string) map[string]bool {
	return collectMCPEnvRefs(configs...)
}

// InspectCredentialUpgradeNames checks potential native file-name conflicts.
// Inputs are the resolver's complete metadata snapshot, not the deduplicated
// public resolve view. It does not establish payload validity or authentication.
func InspectCredentialUpgradeNames(creds []Credential, keeper bool, mcpRefs map[string]bool) []CredentialNameProblem {
	claims := map[string]map[string]string{}
	problems := []CredentialNameProblem{}
	claim := func(name string, c Credential, file bool) {
		if !credname.Valid(name) {
			problems = append(problems, CredentialNameProblem{name, []string{c.ID}, "invalid_name"})
			return
		}
		if file && nativeAliasReserved(name) && !(c.Type == "CLI_TOKEN" && providerAuthSlot(name)) {
			problems = append(problems, CredentialNameProblem{name, []string{c.ID}, "reserved_runtime_name"})
		}
		if claims[name] == nil {
			claims[name] = map[string]string{}
		}
		claims[name][c.ID+"@"+c.EnvVarName] = c.ID
	}
	for _, c := range creds {
		if !credpolicy.Known(c.Type) {
			problems = append(problems, CredentialNameProblem{c.EnvVarName, []string{c.ID}, "unsupported_type"})
			continue
		}
		if c.HandleOnly || (keeper && credpolicy.IsKeeperGated(c.Type)) {
			for _, f := range c.Fields {
				if !f.IsSecret && f.EnvVar != "" {
					claim(f.EnvVar, c, false)
				}
			}
			continue
		}
		p := credpolicy.For(c.Type)
		if p.Delivery == credpolicy.DeliveryNone {
			problems = append(problems, CredentialNameProblem{c.EnvVarName, []string{c.ID}, "unsupported_type"})
			continue
		}
		var fields []string
		for _, f := range c.Fields {
			if f.EnvVar != "" {
				fields = append(fields, f.EnvVar)
			}
		}
		if p.FileMounted() {
			for _, name := range credpolicy.FileAliases(c.Type, c.EnvVarName, fields) {
				claim(name, c, true)
			}
			if c.Type == "GENERIC_SECRET" && mcpRefs[c.EnvVarName] {
				claim(c.EnvVarName+"_PATH", c, true)
			}
		} else {
			// Model login payloads have adapter-specific derivatives, whose
			// conventional names are already reserved against file aliases.
			if c.Type != "PROVIDER_LOGIN" && c.Type != "AI_CLI_TOKEN" && c.EnvVarName != "" {
				claim(c.EnvVarName, c, false)
			}
			for _, name := range fields {
				claim(name, c, false)
			}
		}
	}
	for name, owners := range claims {
		if len(owners) < 2 {
			continue
		}
		ids := []string{}
		for _, id := range owners {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		problems = append(problems, CredentialNameProblem{name, ids, "potential_collision"})
	}
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Alias == problems[j].Alias {
			return problems[i].Reason < problems[j].Reason
		}
		return problems[i].Alias < problems[j].Alias
	})
	return problems
}
