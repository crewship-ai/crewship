package internaltoken

import (
	"strings"
	"testing"
)

// E0: a run-scoped agent token that VERIFIES rather than needing to be
// recognised. See DeriveAgentRunToken's doc for why the difference is the
// whole point.

func TestDeriveAgentRunToken_DistinctPerRun(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"

	a := DeriveAgentRunToken(master, "ws-1", "agent-1", "run-a")
	b := DeriveAgentRunToken(master, "ws-1", "agent-1", "run-b")
	if a == "" || b == "" {
		t.Fatal("token derivation returned empty for valid input")
	}
	// THE defect: two concurrent runs of one agent presented byte-identical
	// credentials under agtv1, so the sidecar could not tell them apart.
	if a == b {
		t.Fatal("two runs of one agent derived the same token — the sidecar cannot attribute a call to a run")
	}
	if v1 := DeriveAgentToken(master, "ws-1", "agent-1"); v1 == a || v1 == b {
		t.Error("a v2 token collided with the v1 token for the same agent")
	}
	// Stable for one run: a token that changed between the env var and the
	// sidecar's copy would authenticate nothing.
	if again := DeriveAgentRunToken(master, "ws-1", "agent-1", "run-a"); again != a {
		t.Errorf("derivation is not deterministic for one run: %q then %q", a, again)
	}
	if !strings.HasPrefix(a, AgentRunPrefix+".") {
		t.Errorf("token %q does not carry the %s prefix", a, AgentRunPrefix)
	}
}

func TestDeriveAgentRunToken_RefusesEmptyInput(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"
	cases := []struct{ name, master, ws, agent, run string }{
		{"empty master", "", "ws-1", "agent-1", "run-a"},
		{"empty workspace", master, "", "agent-1", "run-a"},
		{"empty agent", master, "ws-1", "", "run-a"},
		{"empty run", master, "ws-1", "agent-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveAgentRunToken(c.master, c.ws, c.agent, c.run); got != "" {
				t.Errorf("expected no token for %s, got %q", c.name, got)
			}
		})
	}
}

func TestValidateAgentRunToken_RoundTrip(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"
	tok := DeriveAgentRunToken(master, "ws-1", "agent-1", "run-a")

	ws, agent, run, ok := ValidateAgentRunToken(master, tok)
	if !ok {
		t.Fatal("a freshly derived token failed validation")
	}
	if ws != "ws-1" || agent != "agent-1" || run != "run-a" {
		t.Errorf("round trip = (%q, %q, %q), want (ws-1, agent-1, run-a)", ws, agent, run)
	}
}

// Ids are base64url-encoded rather than embedded literally (agtv1 embeds them
// raw). An id carrying the separator must survive, not reshape the token.
func TestValidateAgentRunToken_IdsWithSeparators(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"
	tok := DeriveAgentRunToken(master, "ws.1", "agent.1", "run.a")
	ws, agent, run, ok := ValidateAgentRunToken(master, tok)
	if !ok {
		t.Fatal("a token whose ids contain dots failed validation")
	}
	if ws != "ws.1" || agent != "agent.1" || run != "run.a" {
		t.Errorf("round trip = (%q, %q, %q), want the dotted originals", ws, agent, run)
	}
}

func TestValidateAgentRunToken_FailsClosed(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"
	const other = "a-completely-different-master-key-value"
	good := DeriveAgentRunToken(master, "ws-1", "agent-1", "run-a")

	cases := []struct{ name, master, token string }{
		{"empty master", "", good},
		{"wrong master", other, good},
		{"empty token", master, ""},
		{"v1 token is not a v2 token", master, DeriveAgentToken(master, "ws-1", "agent-1")},
		{"workspace token", master, DeriveWorkspaceToken(master, "ws-1")},
		{"wrong prefix", master, strings.Replace(good, AgentRunPrefix, "agtv9", 1)},
		{"truncated", master, good[:len(good)-8]},
		{"tampered mac", master, good[:len(good)-1] + "0"},
		{"missing segment", master, AgentRunPrefix + ".dw3tMQ.YWdlbnQtMQ.deadbeef"},
		{"extra segment", master, good + ".extra"},
		{"empty segment", master, AgentRunPrefix + "...."},
		{"not base64", master, AgentRunPrefix + ".!!!.!!!.!!!.deadbeef"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, ok := ValidateAgentRunToken(c.master, c.token); ok {
				t.Errorf("%s validated, want a closed failure", c.name)
			}
		})
	}
}

// A v2 token must not be forgeable by truncating it back to the v1 shape, nor
// the reverse. Domain separation in the MAC context is what guarantees it.
func TestAgentRunToken_DomainSeparatedFromV1(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"
	v1 := DeriveAgentToken(master, "ws-1", "agent-1")
	v1MAC := v1[strings.LastIndex(v1, ".")+1:]
	v2 := DeriveAgentRunToken(master, "ws-1", "agent-1", "run-a")
	v2MAC := v2[strings.LastIndex(v2, ".")+1:]
	if v1MAC == v2MAC {
		t.Error("v1 and v2 share a MAC for the same agent — one could be forged from the other")
	}
}

// Changing ANY bound component changes the token. Otherwise a token minted for
// one agent's run would authenticate another's.
func TestDeriveAgentRunToken_EveryComponentIsBound(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"
	base := DeriveAgentRunToken(master, "ws-1", "agent-1", "run-a")
	for _, c := range []struct{ name, ws, agent, run string }{
		{"workspace", "ws-2", "agent-1", "run-a"},
		{"agent", "ws-1", "agent-2", "run-a"},
		{"run", "ws-1", "agent-1", "run-b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveAgentRunToken(master, c.ws, c.agent, c.run); got == base {
				t.Errorf("changing the %s did not change the token", c.name)
			}
		})
	}
}

// The run key is crew-scoped, exactly like DeriveLLMRouteKey, so a sidecar
// holding one can mint and verify run tokens for ITS crew and nothing else.
// Handing it the master instead would undo the reason sidecarIPCToken is
// crew-derived in the first place.
func TestDeriveAgentRunKey_ScopedAndFailsClosed(t *testing.T) {
	const master = "master-key-that-is-long-enough-for-hmac"

	a := DeriveAgentRunKey(master, "ws-1", "crew-1")
	b := DeriveAgentRunKey(master, "ws-1", "crew-2")
	c := DeriveAgentRunKey(master, "ws-2", "crew-1")
	if a == "" {
		t.Fatal("valid input produced no key")
	}
	if a == b || a == c || b == c {
		t.Error("run keys must differ per workspace and per crew")
	}
	if a != DeriveAgentRunKey(master, "ws-1", "crew-1") {
		t.Error("key derivation is not deterministic")
	}
	// Not the master, and not any other derived token for the same scope.
	if a == master || a == DeriveLLMRouteKey(master, "ws-1", "crew-1") ||
		a == DeriveCrewToken(master, "ws-1", "crew-1") {
		t.Error("the run key collided with the master or another derived value for the same scope")
	}
	for _, bad := range []struct{ name, master, ws string }{
		{"empty master", "", "ws-1"},
		{"empty workspace", master, ""},
	} {
		if got := DeriveAgentRunKey(bad.master, bad.ws, "crew-1"); got != "" {
			t.Errorf("%s produced a key %q, want empty", bad.name, got)
		}
	}

	// A token minted under one crew's key must not verify under another's:
	// that is the containment the crew scoping buys.
	tok := DeriveAgentRunToken(a, "ws-1", "agent-1", "run-a")
	if _, _, _, ok := ValidateAgentRunToken(b, tok); ok {
		t.Error("a run token verified under a DIFFERENT crew's run key")
	}
	if _, _, _, ok := ValidateAgentRunToken(a, tok); !ok {
		t.Error("a run token failed to verify under its own crew's run key")
	}
}
