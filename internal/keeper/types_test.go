package keeper_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
)

func TestDecisionConstants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  keeper.Decision
		want string
	}{
		{"allow", keeper.DecisionAllow, "ALLOW"},
		{"deny", keeper.DecisionDeny, "DENY"},
		{"escalate", keeper.DecisionEscalate, "ESCALATE"},
		{"pending", keeper.DecisionPending, "PENDING"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.got) != tc.want {
				t.Errorf("Decision %q: got %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestSecurityLevelOrdering(t *testing.T) {
	t.Parallel()

	levels := []keeper.SecurityLevel{
		keeper.SecurityLevelL1,
		keeper.SecurityLevelL2,
		keeper.SecurityLevelL3,
		keeper.SecurityLevelL4,
	}
	wantValues := []int{1, 2, 3, 4}
	for i, lvl := range levels {
		if int(lvl) != wantValues[i] {
			t.Errorf("SecurityLevelL%d: got %d, want %d", i+1, lvl, wantValues[i])
		}
	}
	// Strict ordering is a contract: gatekeeper routes L1 → auto-allow vs L3 → LLM vs L4 → human.
	for i := 1; i < len(levels); i++ {
		if !(levels[i-1] < levels[i]) {
			t.Errorf("SecurityLevel not strictly increasing at index %d (%d, %d)",
				i, levels[i-1], levels[i])
		}
	}
}

func TestSecurityLevelJSONEncoding(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(keeper.SecurityLevelL3)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); got != "3" {
		t.Errorf("L3 JSON: got %q, want %q", got, "3")
	}

	var lvl keeper.SecurityLevel
	if err := json.Unmarshal([]byte("2"), &lvl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if lvl != keeper.SecurityLevelL2 {
		t.Errorf("unmarshal 2: got %d, want %d", lvl, keeper.SecurityLevelL2)
	}
}

func TestRequestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 4, 16, 12, 30, 0, 0, time.UTC)
	orig := keeper.Request{
		ID:                "req-abc",
		RequestingAgentID: "agent-1",
		RequestingCrewID:  "crew-eng",
		CredentialID:      "cred-ssh",
		CredentialName:    "deploy-ssh",
		SecurityLevel:     keeper.SecurityLevelL3,
		TaskID:            "task-42",
		Intent:            "deploy to staging",
		WorkspaceID:       "ws-1",
		CreatedAt:         ts,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got keeper.Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, orig.CreatedAt)
	}
	got.CreatedAt = orig.CreatedAt // compared above; allow struct equality below
	if got != orig {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, orig)
	}

	// Wire-format keys are part of the public contract with the sidecar.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	for _, key := range []string{
		"id", "requesting_agent_id", "requesting_crew_id",
		"credential_id", "credential_name", "security_level",
		"task_id", "intent", "workspace_id", "created_at",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("Request JSON missing key %q; got %s", key, data)
		}
	}
}

func TestRequestTaskIDOmitEmpty(t *testing.T) {
	t.Parallel()

	req := keeper.Request{ID: "r1"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["task_id"]; present {
		t.Errorf("empty TaskID must be omitted from JSON; got %s", data)
	}
}

func TestRequestResultJSONRoundTrip(t *testing.T) {
	t.Parallel()

	orig := keeper.RequestResult{
		RequestID: "req-1",
		Decision:  keeper.DecisionAllow,
		Reason:    "low risk",
		RiskScore: 2,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got keeper.RequestResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, orig)
	}
}

// GatekeeperResponse carries the full LLM prompt and raw Ollama output for
// observability. Those fields MUST NOT leak to the sidecar or agent — the
// prompt embeds credential context, and exposing the raw response would let a
// compromised agent learn the Keeper's decision template. The `json:"-"` tag
// enforces that invariant; this test guards it against accidental regressions.
func TestGatekeeperResponseHidesPromptAndRawLLM(t *testing.T) {
	t.Parallel()

	resp := keeper.GatekeeperResponse{
		Decision:       "ALLOW",
		Reason:         "low risk",
		RiskScore:      2,
		Prompt:         "SYSTEM: you are the Keeper ... secret credential X ...",
		RawLLMResponse: `{"decision":"ALLOW","risk":2}`,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(data)

	if strings.Contains(body, "SYSTEM: you are the Keeper") {
		t.Errorf("Prompt content must not be serialised; got %s", body)
	}
	if strings.Contains(strings.ToLower(body), "rawllmresponse") ||
		strings.Contains(body, "raw_llm_response") {
		t.Errorf("RawLLMResponse key must not appear in JSON; got %s", body)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, forbidden := range []string{"Prompt", "prompt", "RawLLMResponse", "raw_llm_response"} {
		if _, present := raw[forbidden]; present {
			t.Errorf("GatekeeperResponse JSON must not include key %q", forbidden)
		}
	}
	// Legit wire keys remain.
	for _, required := range []string{"decision", "reason", "risk"} {
		if _, present := raw[required]; !present {
			t.Errorf("GatekeeperResponse JSON missing required key %q; got %s", required, body)
		}
	}
}

func TestExecuteResultOmitEmpty(t *testing.T) {
	t.Parallel()

	// Deny path: output + exit_code stay empty and must be omitted.
	deny := keeper.ExecuteResult{
		RequestID: "r1",
		Decision:  keeper.DecisionDeny,
		Reason:    "too risky",
		RiskScore: 9,
	}
	data, err := json.Marshal(deny)
	if err != nil {
		t.Fatalf("marshal deny: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal deny: %v", err)
	}
	for _, shouldOmit := range []string{"output", "exit_code"} {
		if _, present := raw[shouldOmit]; present {
			t.Errorf("deny result should omit %q; got %s", shouldOmit, data)
		}
	}
	if raw["reason"] != "too risky" {
		t.Errorf("reason not preserved: got %v", raw["reason"])
	}

	// Allow path: output populated, exit_code 0 is still omitted by `omitempty`.
	allow := keeper.ExecuteResult{
		RequestID: "r2",
		Decision:  keeper.DecisionAllow,
		RiskScore: 1,
		Output:    "ok",
	}
	data, err = json.Marshal(allow)
	if err != nil {
		t.Fatalf("marshal allow: %v", err)
	}
	raw = nil
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal allow: %v", err)
	}
	if raw["output"] != "ok" {
		t.Errorf("output not preserved: got %v", raw["output"])
	}
	if _, present := raw["exit_code"]; present {
		t.Errorf("zero exit_code should be omitted; got %s", data)
	}
}
