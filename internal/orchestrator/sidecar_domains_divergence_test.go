package orchestrator

// The allowlist a crew's shared sidecar runs with is assembled from three
// inputs, and only ONE of them is a crew property:
//
//	req.AllowedDomains          crews.allowed_domains — identical for every member
//	mcpStdioDomains(...)        req.MCPServers, resolved per agent (agent_mcp_bindings)
//	proxiedEndpointDomains(...) the agent's OWN assigned endpoint credential
//
// The last two differ between members of one crew, so each member's exec used
// to compute a different DomainsHash than the sidecar the previous member
// started — and sidecarNeedsRestart says yes to a hash it does not recognise.
// Two members alternating turns therefore killed and relaunched a healthy
// sidecar on EVERY exec: #1160's unconditional restart, arrived at from the
// other direction.
//
// These tests drive the real RunAgent sequence against the payload-derived
// container fake from sidecar_restart_pinning_test.go, so they fail if the
// desired set and the set actually handed to startSidecar ever disagree.

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
)

// divergentRequest is one crew member: same container, same crew-level
// allowlist, its own endpoint credential and its own MCP servers.
func divergentRequest(agent, container string, crewDomains []string, endpointBase string, mcp []MCPServerConfig) AgentRunRequest {
	req := AgentRunRequest{
		AgentID:        "id-" + agent,
		AgentSlug:      agent,
		ChatID:         "chat-" + agent,
		ContainerID:    container,
		CrewID:         "crew-divergence",
		CLIAdapter:     "OPENCODE",
		LLMModel:       "openai_compat/gpt-oss-120b",
		UserMessage:    "go",
		TimeoutSecs:    30,
		NetworkMode:    "restricted",
		AllowedDomains: crewDomains,
		MCPServers:     mcp,
	}
	if endpointBase != "" {
		req.Credentials = []Credential{{
			ID:         "cred-" + agent,
			EnvVarName: "ENDPOINT_" + agent,
			PlainValue: "endpoint-secret-EXAMPLE-NOT-A-REAL-KEY",
			Type:       "API_KEY",
			Provider:   "OPENAI_COMPAT",
			BaseURL:    endpointBase,
		}}
	}
	return req
}

func stdioMCP(name, pkg string) []MCPServerConfig {
	return []MCPServerConfig{{
		ID: name, Name: name, DisplayName: name,
		Transport: "stdio", Command: "npx", Args: []string{"-y", pkg},
	}}
}

// TestRunAgent_PerAgentAllowlistInputs_SidecarConverges is the invariant in
// test form: whatever each member contributes, alternating execs must settle
// on ONE sidecar. The union is reached on the first exec that widens it, and
// every exec after that reuses.
//
// Without the per-container union each of the four execs below computes a hash
// the running sidecar cannot match, so the count is 4 starts / 3 kills — the
// ping-pong, not a convergence.
func TestRunAgent_PerAgentAllowlistInputs_SidecarConverges(t *testing.T) {
	crewDomains := []string{"api.github.com", "registry.npmjs.org"}

	cases := []struct {
		name string
		a, b AgentRunRequest
	}{
		{
			// The #2047 half: an OPENAI_COMPAT credential is granted per
			// agent, so its BaseURL — and therefore the host the sidecar
			// dials — is not a crew property at all.
			name: "endpoint credentials differ",
			a:    divergentRequest("riley", "shared-div1", crewDomains, "https://a.endpoint.example/v1", nil),
			b:    divergentRequest("morgan", "shared-div1", crewDomains, "https://b.endpoint.example/v1", nil),
		},
		{
			// Older than #2047 and the same shape: agent_mcp_bindings is a
			// per-agent opt-out, so two members of one crew resolve different
			// stdio servers and therefore different auto-added API domains.
			name: "stdio MCP servers differ",
			a:    divergentRequest("riley", "shared-div2", crewDomains, "", stdioMCP("linear", "linear-mcp")),
			b:    divergentRequest("morgan", "shared-div2", crewDomains, "", stdioMCP("stripe", "@stripe/mcp")),
		},
		{
			name: "both inputs differ",
			a:    divergentRequest("riley", "shared-div3", crewDomains, "https://a.endpoint.example/v1", stdioMCP("linear", "linear-mcp")),
			b:    divergentRequest("morgan", "shared-div3", crewDomains, "https://b.endpoint.example/v1", stdioMCP("stripe", "@stripe/mcp")),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newPayloadDrivenContainer()
			o := New(payloadDrivenContainerT{fake, t}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			o.SetSidecarEnabled(true)

			runOneAgent(t, o, tc.a)
			if got := atomic.LoadInt32(&fake.starts); got != 1 {
				t.Fatalf("cold start: %d sidecar starts, want 1", got)
			}

			runOneAgent(t, o, tc.b)
			runOneAgent(t, o, tc.a)
			runOneAgent(t, o, tc.b)

			if got := atomic.LoadInt32(&fake.starts); got != 2 {
				t.Fatalf("four alternating execs produced %d sidecar starts, want 2 (cold start + one widening restart) — the crew's members are churning each other's sidecar", got)
			}
			if got := atomic.LoadInt32(&fake.kills); got != 1 {
				t.Fatalf("four alternating execs killed the sidecar %d times, want 1", got)
			}
		})
	}
}

// TestRunAgent_PerAgentAllowlistInputs_SidecarSeesEveryMemberHost pins the
// half a hash-only assertion cannot see: converging on one sidecar is worthless
// if it converged on an allowlist missing a member's host. The sidecar dials
// the upstream on the agent's behalf and checks this list first, so a dropped
// host is a 403 on every model call with a perfectly valid credential.
func TestRunAgent_PerAgentAllowlistInputs_SidecarSeesEveryMemberHost(t *testing.T) {
	crewDomains := []string{"api.github.com"}

	fake := newPayloadDrivenContainer()
	o := New(payloadDrivenContainerT{fake, t}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetSidecarEnabled(true)

	runOneAgent(t, o, divergentRequest("riley", "shared-div4", crewDomains, "https://a.endpoint.example/v1", stdioMCP("linear", "linear-mcp")))
	runOneAgent(t, o, divergentRequest("morgan", "shared-div4", crewDomains, "https://b.endpoint.example/v1", stdioMCP("stripe", "@stripe/mcp")))

	got := fake.lastPolicyDomains("shared-div4")
	for _, want := range []string{
		"api.github.com",
		"a.endpoint.example", "b.endpoint.example",
		"api.linear.app", "api.stripe.com",
	} {
		if !containsDomain(got, want) {
			t.Fatalf("running sidecar's allowlist %v is missing %q — a crew member's traffic is fenced out of its own endpoint", got, want)
		}
	}
}

// TestCrewDesiredDomains covers the accumulator's own rules directly, without
// the RunAgent machinery: what a member contributes, what replaces it, and
// what the next member then sees.
func TestCrewDesiredDomains(t *testing.T) {
	type exec struct {
		container string
		agent     string
		crew      []string
		extras    []string
		want      []string
	}

	cases := []struct {
		name  string
		execs []exec
	}{
		{
			name: "one member contributes to the crew set",
			execs: []exec{
				{"c1", "riley", []string{"api.github.com"}, []string{"a.example"},
					[]string{"api.github.com", "a.example"}},
			},
		},
		{
			// The invariant: after both members have run once, they agree.
			name: "members union and then agree",
			execs: []exec{
				{"c1", "riley", []string{"api.github.com"}, []string{"a.example"},
					[]string{"api.github.com", "a.example"}},
				{"c1", "morgan", []string{"api.github.com"}, []string{"b.example"},
					[]string{"api.github.com", "b.example", "a.example"}},
				{"c1", "riley", []string{"api.github.com"}, []string{"a.example"},
					[]string{"api.github.com", "b.example", "a.example"}},
			},
		},
		{
			// A member's contribution is REPLACED, so revoking its credential
			// narrows the allowlist instead of pinning the old host forever.
			name: "a member narrows its own contribution",
			execs: []exec{
				{"c1", "riley", nil, []string{"a.example"}, []string{"a.example"}},
				{"c1", "morgan", nil, []string{"b.example"}, []string{"b.example", "a.example"}},
				{"c1", "riley", nil, nil, []string{"b.example"}},
			},
		},
		{
			// Containers do not pool: a recreated container gets a new ID and
			// therefore an empty union, which is how a contribution stops
			// outliving the sidecar that might have used it.
			name: "containers are independent",
			execs: []exec{
				{"c1", "riley", nil, []string{"a.example"}, []string{"a.example"}},
				{"c2", "morgan", nil, []string{"b.example"}, []string{"b.example"}},
			},
		},
		{
			// An operator narrowing crews.allowed_domains takes effect on the
			// next exec: the crew half is never accumulated, only the
			// per-agent half is.
			name: "crew-level narrowing takes effect immediately",
			execs: []exec{
				{"c1", "riley", []string{"api.github.com", "api.linear.app"}, []string{"a.example"},
					[]string{"api.github.com", "api.linear.app", "a.example"}},
				{"c1", "riley", []string{"api.github.com"}, []string{"a.example"},
					[]string{"api.github.com", "a.example"}},
			},
		},
		{
			// No container means no shared sidecar to agree with.
			name: "unknown container is passthrough",
			execs: []exec{
				{"", "riley", []string{"api.github.com"}, []string{"a.example"},
					[]string{"api.github.com", "a.example"}},
				{"", "morgan", []string{"api.github.com"}, []string{"b.example"},
					[]string{"api.github.com", "b.example"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Orchestrator{}
			for i, e := range tc.execs {
				got := o.crewDesiredDomains(e.container, e.agent, e.crew, e.extras)
				if DomainsHash(got) != DomainsHash(e.want) {
					t.Fatalf("exec %d (%s in %s): desired domains %v, want %v", i, e.agent, e.container, got, e.want)
				}
			}
		})
	}
}

func containsDomain(domains []string, want string) bool {
	for _, d := range domains {
		if d == want {
			return true
		}
	}
	return false
}
