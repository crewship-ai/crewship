package orchestrator

import (
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/credname"
	"github.com/crewship-ai/crewship/internal/credpolicy"
)

// An OAUTH2 type alone does not identify its value: MCP companions also carry
// client IDs/secrets under that type. Only trusted primary provenance grants
// generic native env delivery. Missing provenance stays MCP-only.
func primaryOAuthMaterial(c Credential, keeper bool, now time.Time) bool {
	p := credpolicy.For(c.Type)
	return c.Type == "OAUTH2" && c.OAuthPrimary && c.ID != "" && !c.HandleOnly && c.PlainValue != "" &&
		p.Delivery == credpolicy.DeliveryEnv && !(keeper && p.KeeperGated) && !credentialLeaseLapsed(c, now)
}

func nativeOAuthSlot(c Credential, keeper bool, now time.Time) (string, bool) {
	if !primaryOAuthMaterial(c, keeper, now) || credentialOAuthKind(c) != oauthNone ||
		!credname.Valid(c.EnvVarName) || nativeAliasReserved(c.EnvVarName) {
		return "", false
	}
	return c.EnvVarName, true
}

// Filter a copy of the request, never the original MCP input. The caller still
// applies explicit MCP references separately, with the original credentials.
func nativeOAuthEnvView(req AgentRunRequest, keeper bool, now time.Time) AgentRunRequest {
	var filtered []Credential
	for i, c := range req.Credentials {
		if c.Type == "OAUTH2" && !primaryOAuthMaterial(c, keeper, now) {
			if filtered == nil {
				filtered = make([]Credential, 0, len(req.Credentials))
				filtered = append(filtered, req.Credentials[:i]...)
			}
			continue
		}
		if filtered != nil {
			filtered = append(filtered, c)
		}
	}
	if filtered != nil {
		req.Credentials = filtered
	}
	return req
}

// Validate declarations before an env overwrite can hide another owner. File
// aliases are validated by appendCredentialFileEnv against the resulting env.
func validateNativeOAuthSlots(env []string, creds []Credential, keeper bool, now time.Time) error {
	for i, c := range creds {
		if !primaryOAuthMaterial(c, keeper, now) || credentialOAuthKind(c) != oauthNone || c.EnvVarName == "" {
			continue
		}
		name := c.EnvVarName
		if !credname.Valid(name) || nativeAliasReserved(name) {
			return fmt.Errorf("OAuth access-token delivery uses invalid or reserved runtime name: %q", name)
		}
		for j, other := range creds {
			if i == j || other.PlainValue == "" || !credEnvDeliverable(other) ||
				(keeper && credpolicy.IsKeeperGated(other.Type)) || fileOnlyCredential(other) {
				continue
			}
			if other.Type == "OAUTH2" && !primaryOAuthMaterial(other, keeper, now) {
				continue
			}
			if credentialOAuthKind(other).fileDelivered() {
				continue
			}
			if resolveEnvVar(&other) == name {
				return fmt.Errorf("credential delivery name collision: %q", name)
			}
		}
		if envHasName(env, name) && !envHasAssignment(env, name, c.PlainValue) {
			return fmt.Errorf("credential delivery name collision: %q", name)
		}
	}
	return nil
}
