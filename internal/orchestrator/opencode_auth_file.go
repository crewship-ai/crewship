package orchestrator

import "encoding/json"

// OpenCodeAuthFileRel is relative to the agent's isolated HOME. Schema:
// github.com/anomalyco/opencode/packages/opencode/src/auth/index.ts (ApiAuth).
const OpenCodeAuthFileRel = ".local/share/opencode/auth.json"

func renderOpenCodeAuth(req AgentRunRequest) ([]byte, error) {
	providers := map[string]string{
		"ANTHROPIC_API_KEY": "anthropic", "OPENAI_API_KEY": "openai",
		"GOOGLE_API_KEY": "google", "GEMINI_API_KEY": "google",
		"OPENROUTER_API_KEY": "openrouter", "XAI_API_KEY": "xai",
		"GROQ_API_KEY": "groq", "DEEPSEEK_API_KEY": "deepseek",
		"MOONSHOT_API_KEY": "moonshotai", "ZAI_API_KEY": "zai",
		"MINIMAX_API_KEY": "minimax",
	}
	type apiAuth struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	auth := map[string]apiAuth{}
	routed, isRouted := resolveRoutedProvider(req, req.sidecarActive)
	for _, cred := range req.Credentials {
		id, known := providers[cred.EnvVarName]
		if !known || cred.HandleOnly || cred.PlainValue == "" || !credEnvDeliverable(cred) || credentialOAuthKind(cred) != oauthNone {
			continue
		}
		// The delivery resolver already orders grants by priority. A second
		// key for the same provider must not silently replace the winner.
		if _, exists := auth[id]; exists {
			continue
		}
		key := cred.PlainValue
		if isRouted && credentialRoutesTo(cred, routed.Spec) {
			key = "dummy-crewship-sidecar"
		}
		auth[id] = apiAuth{Type: "api", Key: key}
	}
	// Always replace the complete file, even with {} after unassignment.
	// Never merge in stale authorizations from an earlier run.
	return json.Marshal(auth)
}
