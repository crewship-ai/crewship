package orchestrator

// #2052 — the boot payload has to say WHO each credential was granted to.
//
// The sidecar's CredStore is crew-wide (one sidecar per crew container) and now
// refuses to serve a credential to a member it does not name. That refusal is
// only as good as the ownership the orchestrator delivers, and the delivery has
// two properties that pull against each other:
//
//   - it must be present, so a per-agent grant stops being crew-wide in effect;
//   - it must be IDENTICAL no matter which member's exec built the payload,
//     because it feeds sidecarConfigFingerprint and a fingerprint that moved per
//     member would restart the shared sidecar on every alternation — the exact
//     thrash #1160 removed.

import (
	"encoding/json"
	"testing"
)

func TestBuildSidecarCreds_CarriesPerAgentOwnership(t *testing.T) {
	tests := []struct {
		name  string
		creds []Credential
		want  map[string][]string // credential id -> agent ids on the payload
	}{
		{
			name: "an agent-scoped grant names its grantees",
			creds: []Credential{{
				ID: "compat-a", EnvVarName: "", Provider: "OPENAI_COMPAT",
				PlainValue: "sk-a", BaseURL: "https://a.example/v1",
				AgentIDs: []string{"agt_a"},
			}},
			want: map[string][]string{"compat-a": {"agt_a"}},
		},
		{
			// The crew-wide case is the one that must serialise EXACTLY as it
			// did before ownership existed, or every existing crew's config
			// fingerprint moves and every shared sidecar restarts once.
			name: "a crew-scoped credential names nobody",
			creds: []Credential{{
				ID: "ant-crew", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant",
			}},
			want: map[string][]string{"ant-crew": nil},
		},
		{
			name: "a credential granted to several members carries all of them",
			creds: []Credential{{
				ID: "shared", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant",
				AgentIDs: []string{"agt_b", "agt_a"},
			}},
			// Sorted on the way out: the set is the same for every member, so
			// its ENCODING has to be too.
			want: map[string][]string{"shared": {"agt_a", "agt_b"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := buildSidecarCreds(tt.creds, nil)
			if len(sc) != len(tt.want) {
				t.Fatalf("buildSidecarCreds returned %d credentials, want %d", len(sc), len(tt.want))
			}
			for _, got := range sc {
				want, ok := tt.want[got.ID]
				if !ok {
					t.Fatalf("unexpected credential %q on the boot payload", got.ID)
				}
				if len(got.AgentIDs) != len(want) {
					t.Fatalf("%s: agent_ids = %v, want %v", got.ID, got.AgentIDs, want)
				}
				for i := range want {
					if got.AgentIDs[i] != want[i] {
						t.Fatalf("%s: agent_ids = %v, want %v", got.ID, got.AgentIDs, want)
					}
				}
			}
		})
	}
}

// The wire key the sidecar reads, and — for a crew-wide credential — its
// ABSENCE. sidecarCred and sidecar.Credential are joined only by these strings
// (see TestSidecarCredWireTags), and omitempty here is what keeps an existing
// crew's payload byte-identical.
func TestSidecarCred_AgentIDsWireShape(t *testing.T) {
	scoped, err := json.Marshal(sidecarCred{ID: "c1", Provider: "OPENAI_COMPAT", Token: "t", AgentIDs: []string{"agt_a"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(scoped, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids, ok := got["agent_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "agt_a" {
		t.Errorf("agent_ids on the wire = %v, want [agt_a]", got["agent_ids"])
	}

	crewWide, err := json.Marshal(sidecarCred{ID: "c1", Provider: "ANTHROPIC", Token: "t"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var plain map[string]any
	if err := json.Unmarshal(crewWide, &plain); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := plain["agent_ids"]; present {
		t.Error("a crew-wide credential emitted agent_ids: every existing crew's " +
			"config fingerprint moves and every shared sidecar restarts once")
	}
}

// The anti-thrash property, stated as a test rather than as a comment: two
// members of one crew produce the SAME fingerprint for the same credential set,
// so neither member's exec restarts the sidecar the other is using. The API
// tier computes the grantee set per credential — not "the agent whose exec
// booted the sidecar" — which is what makes that true.
func TestSidecarConfigFingerprint_StableAcrossCrewMembers(t *testing.T) {
	const key = "master-internal-token-for-this-test"
	shared := []Credential{
		{ID: "ant", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant", AgentIDs: []string{"agt_a", "agt_b"}},
		{ID: "crew", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-oai"},
	}
	// The same delivery as another member would see it: same set, different
	// order (the delivery query orders by priority then source rank) and the
	// grantee list in the other order too.
	asPeerSees := []Credential{
		{ID: "crew", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-oai"},
		{ID: "ant", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant", AgentIDs: []string{"agt_b", "agt_a"}},
	}
	if a, b := sidecarConfigFingerprint(key, shared), sidecarConfigFingerprint(key, asPeerSees); a != b {
		t.Errorf("fingerprint differs per member (%s vs %s): each exec would restart "+
			"the shared sidecar the other is using", a, b)
	}

	// And it DOES move when ownership genuinely changes — otherwise a sidecar
	// booted with the old grants keeps serving them after a revoke.
	regranted := []Credential{
		{ID: "ant", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant", AgentIDs: []string{"agt_a"}},
		{ID: "crew", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-oai"},
	}
	if a, b := sidecarConfigFingerprint(key, shared), sidecarConfigFingerprint(key, regranted); a == b {
		t.Error("narrowing a grant to one member left the fingerprint unchanged: " +
			"the running sidecar keeps serving the credential to the member that lost it")
	}
}
