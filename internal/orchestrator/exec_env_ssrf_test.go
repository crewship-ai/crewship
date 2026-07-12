package orchestrator

import "testing"

// TestLocalModelExtraDomains_SSRF locks the host-side run-time gate (#961):
// a literal private-range endpoint is only allowlisted when the crew opted in;
// a literal metadata/link-local endpoint is refused even with the opt-in; a
// non-literal hostname is passed through for the sidecar to resolve-and-check.
func TestLocalModelExtraDomains_SSRF(t *testing.T) {
	mk := func(url string, optIn bool) AgentRunRequest {
		return AgentRunRequest{
			CLIAdapter:            "OPENCODE",
			LLMModel:              "ollama/qwen2.5-coder:7b",
			LocalModelBaseURL:     url,
			AllowPrivateEndpoints: optIn,
		}
	}

	cases := []struct {
		name  string
		url   string
		optIn bool
		want  []string
	}{
		{"private literal, opt-in off → blocked", "http://192.168.1.222:11434/v1", false, nil},
		{"private literal, opt-in on → allowed", "http://192.168.1.222:11434/v1", true, []string{"192.168.1.222"}},
		{"loopback literal, opt-in on → allowed", "http://127.0.0.1:11434/v1", true, []string{"127.0.0.1"}},
		{"metadata literal, opt-in off → blocked", "http://169.254.169.254/v1", false, nil},
		{"metadata literal, opt-in ON → still blocked", "http://169.254.169.254/v1", true, nil},
		{"public literal → allowed", "https://203.0.113.9/v1", true, nil}, // 203.0.113/24 is TEST-NET-3 (hard-blocked)
		{"hostname passes through (sidecar resolves)", "http://host.docker.internal:11434/v1", false, []string{"host.docker.internal"}},
		{"public hostname passes through", "https://llm.example.com/v1", false, []string{"llm.example.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := localModelExtraDomains(mk(c.url, c.optIn))
			if len(got) != len(c.want) {
				t.Fatalf("localModelExtraDomains = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("localModelExtraDomains = %v, want %v", got, c.want)
				}
			}
		})
	}
}
