package api

// #1648, the agent-facing half.
//
// Every other surface this issue touched reports network_mode to a HUMAN. This
// one reports it to the model, at every run, and the model acts on it: an agent
// that believes its egress is fenced treats an outbound call as contained and
// can skip a precaution because "the allowlist will catch it". A stale badge
// costs a person one bad decision; a stale claim in the prompt costs a bad
// action, repeatedly, unattended.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func egressCfgRig(t *testing.T, networkMode string) (h *InternalHandler, db *sql.DB, agentID string) {
	t.Helper()
	ensureEncryptionKey(t)
	db = setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-egr", wsID, "Egr", "egr")
	if _, err := db.Exec(`UPDATE crews SET network_mode = ? WHERE id = ?`, networkMode, crewID); err != nil {
		t.Fatalf("set network_mode: %v", err)
	}
	agentID = seedAgentRow(t, db, "agent-egr", wsID, crewID, "Egr", "egr-agent", "AGENT")
	h = NewInternalHandler(db, "tok", newTestLogger())
	return h, db, agentID
}

func resolveAgentJSON(t *testing.T, h *InternalHandler, agentID string) map[string]any {
	t.Helper()
	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func promptFor(t *testing.T, h *InternalHandler, agentID string) string {
	t.Helper()
	p, _ := resolveAgentJSON(t, h, agentID)["system_prompt"].(string)
	return p
}

// TestAgentConfig_UnenforcedEgressTellsTheAgentToActAsIfOpen is the assertion
// that matters. The agent must not merely be informed that the mode is
// unenforced — it must be instructed in a way that changes what it does.
func TestAgentConfig_UnenforcedEgressTellsTheAgentToActAsIfOpen(t *testing.T) {
	h, _, agentID := egressCfgRig(t, "restricted")
	h.SetContainer(noEgressProvider{})

	resp := resolveAgentJSON(t, h, agentID)

	// The machine-readable half: the config stops asserting an unqualified fence.
	if resp["network_mode"] != "restricted" {
		t.Fatalf("network_mode = %v, want the configured intent unchanged", resp["network_mode"])
	}
	enforced, ok := resp["network_mode_enforced"].(bool)
	if !ok {
		t.Fatalf("network_mode_enforced missing from the agent config: %#v", resp)
	}
	if enforced {
		t.Fatal("the agent must not be handed network_mode_enforced=true when the provider drops it")
	}
	if reason, _ := resp["network_mode_unenforced_reason"].(string); !strings.Contains(reason, "crewship-sidecar") {
		t.Errorf("the provider's own reason must ride along, got %q", reason)
	}

	// The behavioural half: the prompt has to read as instructions.
	prompt, _ := resp["system_prompt"].(string)
	if !strings.Contains(prompt, "[NETWORK POLICY — NOT ENFORCED]") {
		t.Fatalf("system_prompt is missing the network-policy block:\n%s", prompt)
	}
	// Each of these is a behaviour the agent would otherwise get wrong, and
	// each is phrased as something to DO rather than something that is true.
	// The framing line is listed first and deliberately: a block that opens
	// by stating a fact ("Network mode: restricted (not enforced)") has
	// informed the model; one that opens by telling it how to work has
	// changed what it does, and that difference is the entire point of
	// putting this in the prompt rather than only in the JSON.
	for _, want := range []string{
		"Work as if you are on an open, unmonitored network",
		"configured for restricted egress, but the container runtime it is running on cannot apply that setting",
		"Assume every outbound request succeeds and leaves this machine",
		"Redact secrets, tokens and customer data before sending anything",
		"Do not describe this crew to the user as network-restricted",
		"say so instead of proceeding",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the block must instruct the agent to %q; prompt block was:\n%s",
				want, prompt[strings.Index(prompt, "[NETWORK POLICY"):])
		}
	}
	// And it must carry the reason, so the agent can explain the situation
	// rather than just asserting it.
	if !strings.Contains(prompt, "crewship-sidecar") {
		t.Error("the block must carry the provider's reason")
	}
}

// TestAgentConfig_FencedCrewPromptIsByteIdentical is the guard on the other
// side. There is no network block in the prompt today, so a crew whose mode IS
// enforced must produce exactly the bytes it produced before this existed —
// otherwise every Docker user pays prompt tokens, and a prompt diff, for a
// problem they do not have.
//
// Byte-equality is asserted against two independent baselines that both
// predate this change in behaviour: an instance with no container provider
// wired at all (nothing to consult), and a provider that reports no drop.
func TestAgentConfig_FencedCrewPromptIsByteIdentical(t *testing.T) {
	// Baseline: today's behaviour — nothing consulted.
	hNil, _, agentNil := egressCfgRig(t, "restricted")
	baseline := promptFor(t, hNil, agentNil)

	// Validate the yardstick BEFORE measuring against it. A baseline that
	// itself grew the block would make every comparison below pass while the
	// prompt changed for everyone — which is exactly what a mutation that
	// emits the block unconditionally does.
	for _, marker := range []string{"NETWORK POLICY", "NOT ENFORCED", "not enforced", "crewship-sidecar"} {
		if strings.Contains(baseline, marker) {
			t.Fatalf("baseline prompt must contain no network-policy trace, found %q:\n%s", marker, baseline)
		}
	}

	t.Run("provider that enforces the mode", func(t *testing.T) {
		h, _, agentID := egressCfgRig(t, "restricted")
		h.SetContainer(egressFakeProvider{})
		got := promptFor(t, h, agentID)
		if got != baseline {
			t.Fatalf("prompt for an ENFORCED crew must be byte-identical to the no-provider baseline.\n"+
				"len(got)=%d len(want)=%d\n--- got ---\n%s\n--- want ---\n%s",
				len(got), len(baseline), got, baseline)
		}
	})

	t.Run("free crew on a provider that cannot fence", func(t *testing.T) {
		h, _, agentID := egressCfgRig(t, "free")
		h.SetContainer(noEgressProvider{})
		got := promptFor(t, h, agentID)
		if got != baseline {
			t.Fatalf("a free crew asks for no fence, so its prompt must match the baseline too.\n"+
				"--- got ---\n%s\n--- want ---\n%s", got, baseline)
		}
	})
}

// TestAgentConfig_EnforcedCrewPayloadSaysSoWithoutAReason: the flag is always
// present (an absent key reads as "yes", which is the assumption the whole
// defect was made of), and the reason is absent when there is nothing to say.
func TestAgentConfig_EnforcedCrewPayloadSaysSoWithoutAReason(t *testing.T) {
	h, _, agentID := egressCfgRig(t, "restricted")
	h.SetContainer(egressFakeProvider{})

	resp := resolveAgentJSON(t, h, agentID)
	enforced, ok := resp["network_mode_enforced"].(bool)
	if !ok {
		t.Fatalf("network_mode_enforced must always be present: %#v", resp)
	}
	if !enforced {
		t.Fatal("a provider that reports no drop enforces the mode")
	}
	if _, present := resp["network_mode_unenforced_reason"]; present {
		t.Error("no reason belongs on an enforced payload")
	}
}

// TestBuildNetworkPolicyBlock_EmptyWheneverEnforced is the unit-level pin on
// the empty return the byte-identity above depends on.
func TestBuildNetworkPolicyBlock_EmptyWheneverEnforced(t *testing.T) {
	if got := buildNetworkPolicyBlock("restricted", crewEgressEnforcement{Enforced: true}); got != "" {
		t.Errorf("enforced must render nothing, got %q", got)
	}
	// Even if a reason somehow rides along, Enforced wins — the block exists
	// to describe an absent control, not to narrate a present one.
	if got := buildNetworkPolicyBlock("restricted", crewEgressEnforcement{Enforced: true, Reason: "x"}); got != "" {
		t.Errorf("enforced must render nothing even with a reason, got %q", got)
	}
	got := buildNetworkPolicyBlock("restricted", crewEgressEnforcement{Enforced: false, Reason: "no proxy binary"})
	if !strings.HasPrefix(got, "[NETWORK POLICY — NOT ENFORCED]") || !strings.HasSuffix(got, "[END NETWORK POLICY]") {
		t.Errorf("block must be a delimited section like its siblings, got:\n%s", got)
	}
	if !strings.Contains(got, "no proxy binary") {
		t.Errorf("block must carry the reason, got:\n%s", got)
	}
	// A provider that reports a drop with no detail still gets a usable block.
	bare := buildNetworkPolicyBlock("restricted", crewEgressEnforcement{Enforced: false})
	if !strings.Contains(bare, "cannot apply") {
		t.Errorf("a reasonless drop must still say what is wrong, got:\n%s", bare)
	}
}
