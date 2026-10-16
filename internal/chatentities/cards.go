package chatentities

import "encoding/json"

// These are read projections, not independent proposal/decision state stores.
// Source readers own authorization and map authoritative state into this shape.
const CardSchemaVersion = 1
const MaxProposalItems = 20

type ProposalItem struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	CrewID      string  `json:"crew_id"`
	ProjectID   *string `json:"project_id"`
	AssigneeID  *string `json:"assignee_id"`
	Priority    string  `json:"priority"`
	Selected    bool    `json:"selected"`
}
type CreatedIssueRef struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
}
type ProposalOutcome struct {
	ReviewedVersion int64             `json:"reviewed_version"`
	ConfirmedBy     string            `json:"confirmed_by"`
	ConfirmedAt     string            `json:"confirmed_at"`
	Issues          []CreatedIssueRef `json:"issues"`
}
type ProposalCard struct {
	SchemaVersion  int              `json:"schema_version"`
	Version        int64            `json:"version"`
	Status         string           `json:"status"`
	Title          string           `json:"title"`
	Items          []ProposalItem   `json:"items"`
	ExpiresAt      string           `json:"expires_at"`
	AllowedActions []string         `json:"allowed_actions"`
	Outcome        *ProposalOutcome `json:"outcome"`
}

// Public commands carry intent/version only. Identity and provenance never
// come from these bodies. Mutations require an Idempotency-Key header.
type ProposalCreate struct {
	Title string         `json:"title"`
	Items []ProposalItem `json:"items"`
}
type ProposalEdit struct {
	ExpectedVersion int64          `json:"expected_version"`
	Title           string         `json:"title"`
	Items           []ProposalItem `json:"items"`
}
type ProposalConfirm struct {
	ProposalID      string `json:"proposal_id"`
	ReviewedVersion int64  `json:"reviewed_version"`
}
type ProposalDiscard struct {
	ExpectedVersion int64 `json:"expected_version"`
}
type DecisionAnswer struct {
	SourceID        string                     `json:"source_id"`
	ExpectedVersion string                     `json:"expected_version"`
	ActionID        string                     `json:"action_id,omitempty"`
	Action          string                     `json:"action"`
	Values          map[string]json.RawMessage `json:"values"`
}

// Version is opaque: an adapter may use an existing revision or a fingerprint
// of authoritative source state. Never create a second answer-version ledger.
type DecisionSource struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version string `json:"version"`
}
type DecisionOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}
type DecisionField struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Label    string           `json:"label"`
	Required bool             `json:"required"`
	Options  []DecisionOption `json:"options,omitempty"`
}
type DecisionWorkflowAction struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Approved bool   `json:"approved"`
}

type DecisionResolution struct {
	ActionID   string                     `json:"action_id,omitempty"`
	AnsweredBy string                     `json:"answered_by"`
	AnsweredAt string                     `json:"answered_at"`
	Values     map[string]json.RawMessage `json:"values"`
}
type DecisionCard struct {
	SchemaVersion   int                      `json:"schema_version"`
	Source          DecisionSource           `json:"source"`
	DecisionType    string                   `json:"decision_type"`
	Status          string                   `json:"status"`
	Title           string                   `json:"title"`
	WorkflowActions []DecisionWorkflowAction `json:"workflow_actions,omitempty"`
	Fields          []DecisionField          `json:"fields"`
	ExpiresAt       *string                  `json:"expires_at"`
	AllowedActions  []string                 `json:"allowed_actions"`
	Resolution      *DecisionResolution      `json:"resolution"`
}
