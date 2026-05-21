package keeper

import "time"

// Decision represents a Keeper access decision.
type Decision string

const (
	DecisionAllow    Decision = "ALLOW"
	DecisionDeny     Decision = "DENY"
	DecisionEscalate Decision = "ESCALATE"
	DecisionPending  Decision = "PENDING"
)

// RequestType enumerates the six valid keeper_requests.request_type
// values landed by migration v100 (PRD §6 F4). The DB CHECK constraint
// enforces the same closed set — adding a value here without extending
// the migration's CHECK (and the Gatekeeper buildPrompt switch) is a
// silent regression. The string values match the SQL literals exactly.
type RequestType string

const (
	// RequestTypeAccess is the original credential-read request type
	// (v9). The agent asks the Keeper "may I read credential X for
	// purpose Y?" Most common path.
	RequestTypeAccess RequestType = "access"
	// RequestTypeExecute is the credential-bound command execution type
	// (v10). The agent asks "may I run command C with credential X?"
	// Output is scrubbed of the credential value before being returned.
	RequestTypeExecute RequestType = "execute"
	// RequestTypeSkillReview (F4.1) is the per-skill audit type. The
	// Curator aux slot evaluates whether a skill should stay verified
	// based on assignment + usage + failure aggregates.
	RequestTypeSkillReview RequestType = "skill_review"
	// RequestTypeBehavior (F4.2) is the sampled post-tool-call behavior
	// monitor type. Runs the Behavior aux slot. DENY semantics depend
	// on crews.behavior_mode (warn → non-blocking inbox; block →
	// BlockedError + blocking inbox).
	RequestTypeBehavior RequestType = "behavior"
	// RequestTypeMemoryHealth (F4.3) is the daily AGENT.md / CREW.md
	// hygiene sweep type. Reads consolidate.ComputeHealth + the
	// memory_relations refutes count. DENY auto-triggers consolidation.
	RequestTypeMemoryHealth RequestType = "memory_health"
	// RequestTypeNegativeLearning (F4.4) is the failure-driven lessons
	// writer type. Consumes consolidate.lesson_writer with
	// LessonKindNegative. First real consumer of Z.7's primitive.
	RequestTypeNegativeLearning RequestType = "negative_learning"
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
//
// RequestType is the closed-set selector landed by migration v100 —
// the F4 evaluators inspect it to pick the right prompt template.
// Empty defaults to RequestTypeAccess at the gatekeeper layer for
// backwards-compat with pre-F4 callers.
type Request struct {
	ID                string        `json:"id"`
	RequestingAgentID string        `json:"requesting_agent_id"`
	RequestingCrewID  string        `json:"requesting_crew_id"`
	CredentialID      string        `json:"credential_id"`
	CredentialName    string        `json:"credential_name"`
	SecurityLevel     SecurityLevel `json:"security_level"`
	TaskID            string        `json:"task_id,omitempty"`
	Intent            string        `json:"intent"`
	WorkspaceID       string        `json:"workspace_id"`
	CreatedAt         time.Time     `json:"created_at"`
	RequestType       RequestType   `json:"request_type,omitempty"`
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
