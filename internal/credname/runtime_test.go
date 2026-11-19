package credname

import "testing"

func TestRuntimeReservedKeepsProxyAndLoaderConfigurationPlatformOwned(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HtTp_PrOxY", "ALL_PROXY", "ftp_proxy", "NO_PROXY", "NODE_USE_ENV_PROXY", "HOME", "PATH", "SHELL", "IFS", "ENV", "BASH_ENV", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "CREWSHIP_AGENT_TOKEN", "OPENAI_BASE_URL", "CODEX_HOME", "OPENCODE_CONFIG_CONTENT"} {
		if !RuntimeReserved(name) {
			t.Errorf("runtime name %s was not reserved", name)
		}
	}
	for _, name := range []string{"GITLAB_TOKEN", "GH_TOKEN", "OPENAI_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "MY_SECRET", "PROXY_TOKEN"} {
		if RuntimeReserved(name) {
			t.Errorf("ordinary Keeper token slot %s was reserved", name)
		}
	}
}
