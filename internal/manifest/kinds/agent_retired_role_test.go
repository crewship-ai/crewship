package kinds

import (
	"strings"
	"testing"
)

// COORDINATOR is retired, and until #2195 the standalone `kind: Agent`
// validator said the opposite — twice.
//
// `validAgentRoles` admitted the literal, so `crewship apply --dry-run`
// printed a green plan ("Plan: 1 to create") for a document the server then
// refused with `400 {"error":"agent_role must be AGENT or LEAD"}`
// (internal/api/agents.go:186, pinned by agents_test.go as "retired in v0.1").
// A dry-run plan is the artifact CI checks, so the lie was load-bearing. And
// the enum error message for *every other* bad role advertised COORDINATOR as
// one of the values that work, so a user who mistyped the role was handed a
// fix that fails.
//
// Validate takes a WorkspaceContext and no client — it cannot make a request —
// so a refusal here is by construction a refusal before any HTTP round-trip.
// That is the property these tests pin: the plan must never promise a create
// the server will reject.
//
// Sibling of #2166 (create-agent dialog) and #2189/#2191, which made
// `crewship agent create --role COORDINATOR` refuse locally with a message
// naming the retirement and LEAD. This is the same refusal one layer over.

func TestAgent_Validate_RefusesRetiredCoordinatorRole(t *testing.T) {
	// The API compares the role case-insensitively when it rejects it, and a
	// hand-written manifest is as likely to carry `coordinator` as the
	// screaming form. Both are the retired role and both get the message that
	// carries the fix, not the generic "invalid agent_role" bucket.
	cases := []struct {
		name string
		role string
	}{
		{name: "canonical spelling", role: "COORDINATOR"},
		{name: "lowercase", role: "coordinator"},
		{name: "title case", role: "Coordinator"},
		{name: "mixed case", role: "CoOrDiNaToR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := agentSampleDoc()
			doc.Spec.AgentRole = tc.role

			err := doc.Validate(agentCtxWithCrew("engineering"))
			if err == nil {
				t.Fatalf("agent_role %q must be refused locally; the server answers 400 for it", tc.role)
			}
			msg := err.Error()
			// The message has to carry the fix. The server's own 400 already
			// said "must be AGENT or LEAD" and that was not enough to tell a
			// template author what had changed under them.
			if !strings.Contains(strings.ToUpper(msg), "COORDINATOR") {
				t.Errorf("refusal must name the role the manifest used, got %q", msg)
			}
			if !strings.Contains(strings.ToUpper(msg), "LEAD") {
				t.Errorf("refusal must name LEAD as the replacement, got %q", msg)
			}
			if !strings.Contains(msg, "v0.1") {
				t.Errorf("refusal must say the role was retired in v0.1, got %q", msg)
			}
		})
	}
}

func TestAgent_Validate_SupportedRolesStillAccepted(t *testing.T) {
	// The refusal must not turn into a stricter role validator. AGENT and
	// LEAD are what the server enum holds, and an empty value is the
	// "let the server default to AGENT" path the schema documents.
	for _, role := range []string{"AGENT", "LEAD", ""} {
		t.Run("role="+role, func(t *testing.T) {
			doc := agentSampleDoc()
			doc.Spec.AgentRole = role

			if err := doc.Validate(agentCtxWithCrew("engineering")); err != nil {
				t.Fatalf("agent_role %q must still validate, got %v", role, err)
			}
		})
	}
}

func TestAgent_Validate_EnumErrorDoesNotAdvertiseRetiredRole(t *testing.T) {
	// The second half of #2195: the enum error for an unrelated typo listed
	// COORDINATOR among the values to use. A user who wrote BOSS was told to
	// write COORDINATOR, and that fails at apply with a server 400 — the
	// message recommended the exact bug this issue is about.
	doc := agentSampleDoc()
	doc.Spec.AgentRole = "BOSS"

	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil {
		t.Fatal("want an enum error for agent_role BOSS")
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid agent_role") {
		t.Fatalf("want agent_role enum error, got %v", err)
	}
	if strings.Contains(strings.ToUpper(msg), "COORDINATOR") {
		t.Errorf("the enum error must not offer a role the server rejects, got %q", msg)
	}
}
