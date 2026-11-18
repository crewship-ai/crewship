package credpolicy

import "strings"

// TokenProbeAllowed permits only scalar credentials understood by provider
// authentication probes. Delivery and probe support are separate policies:
// files, passwords and structured OAuth logins must never be forwarded as API
// tokens. PROVIDER_LOGIN callers must first resolve its persisted login mode.
func TokenProbeAllowed(credType, provider string) bool {
	switch credType {
	case "API_KEY", "CLI_TOKEN":
		return true
	case "AI_CLI_TOKEN":
		// Legacy Codex and Gemini logins contain structured login artifacts.
		// Claude setup tokens retain their dedicated OAuth probe.
		return !strings.EqualFold(provider, "OPENAI") && !strings.EqualFold(provider, "GOOGLE")
	default:
		return false
	}
}
