package credname

import "strings"

// ProxyEnvironment reports names interpreted as proxy configuration by crew
// tools. Some clients fold case; curl also supports ALL_PROXY. Platform egress
// configuration must not be replaced by a credential slot or routine input.
func ProxyEnvironment(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "_proxy")
}

// RuntimeReserved reports platform-owned runtime/configuration names. It does
// not reserve provider token names: an explicitly gated Keeper child may need
// OPENAI_API_KEY, for example, while the main adapter reserves those separately.
func RuntimeReserved(name string) bool {
	if ProxyEnvironment(name) || strings.HasPrefix(name, "CREWSHIP_") || strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") {
		return true
	}
	switch name {
	case "HOME", "PATH", "SHELL", "IFS", "ENV", "BASH_ENV", "CODEX_HOME", "GEMINI_CLI_SYSTEM_SETTINGS_PATH", "OPENCODE_CONFIG_CONTENT", "DISABLE_AUTOUPDATER", "ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "GOOGLE_GEMINI_BASE_URL":
		return true
	}
	return false
}
