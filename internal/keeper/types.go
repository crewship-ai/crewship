package keeper

import "time"

// Decision represents a Keeper access decision.
type Decision string

const (
	DecisionAllow   Decision = "ALLOW"
	DecisionDeny    Decision = "DENY"
	DecisionEscalate Decision = "ESCALATE"
	DecisionPending Decision = "PENDING"
)

// SecurityLevel classifies how sensitive a credential is.
// L1 = low (npm tokens, read-only APIs)
// L2 = medium (GitHub write, DB read)
// L3 = high (SSH, DB admin, AWS)
// L4 = critical (production admin, payment) — human approval, future work
type SecurityLevel int

const (
	SecurityLevelL1 SecurityLevel = 1
	SecurityLevelL2 SecurityLevel = 2
	SecurityLevelL3 SecurityLevel = 3
	SecurityLevelL4 SecurityLevel = 4
)

// Request is a credential access request from an agent, forwarded via the sidecar.
type Request struct {
	ID                 string    `json:"id"`
	RequestingAgentID  string    `json:"requesting_agent_id"`
	RequestingCrewID   string    `json:"requesting_crew_id"`
	CredentialID       string    `json:"credential_id"`
	CredentialName     string    `json:"credential_name"`
	SecurityLevel      SecurityLevel `json:"security_level"`
	TaskID             string    `json:"task_id,omitempty"`
	Intent             string    `json:"intent"`
	WorkspaceID        string    `json:"workspace_id"`
	CreatedAt          time.Time `json:"created_at"`
}

// RequestResult is the outcome returned to the sidecar / agent.
type RequestResult struct {
	RequestID string   `json:"request_id"`
	Decision  Decision `json:"decision"`
	Reason    string   `json:"reason"`
	RiskScore int      `json:"risk_score"`
}

// GatekeeperResponse is the JSON structure returned by the Keeper Agent LLM.
type GatekeeperResponse struct {
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	RiskScore int    `json:"risk"`

	// Prompt is the full text sent to Ollama (populated for observability, not serialised to the agent).
	Prompt string `json:"-"`
	// RawLLMResponse is the verbatim text returned by Ollama before JSON parsing.
	RawLLMResponse string `json:"-"`
}

// ExecuteResult is returned by /keeper/execute after the command has been
// evaluated and (if allowed) executed inside the agent container.
// The output is scrubbed of the credential value before being returned.
type ExecuteResult struct {
	RequestID string   `json:"request_id"`
	Decision  Decision `json:"decision"`
	Reason    string   `json:"reason,omitempty"`
	RiskScore int      `json:"risk_score"`
	Output    string   `json:"output,omitempty"`
	ExitCode  int      `json:"exit_code,omitempty"`
}
