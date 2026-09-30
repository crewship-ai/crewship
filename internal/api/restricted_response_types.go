package api

import "github.com/crewship-ai/crewship/internal/access"

// These DTOs are the wire source for generated restricted response contracts.
// They contain only the authorized projection, never runtime capabilities.
type restrictedAgentProfileResponse struct {
	Profile string `json:"profile"`
}
type restrictedOperationModeResponse struct {
	Mode string `json:"mode"`
}
type restrictedChatModeResponse struct {
	Mode     string `json:"mode"`
	Audience string `json:"audience"`
}
type restrictedCLIContextResponse struct {
	ID string `json:"id"`
}
type restrictedContextListResponse struct {
	Entries []access.ContextEntry `json:"entries"`
	Limit   int                   `json:"limit"`
}
type restrictedOutputFilesResponse struct {
	Files []access.FileVersion `json:"files"`
}
type restrictedPageWorkflowStatusResponse struct {
	PendingID             string            `json:"pending_id"`
	RunID                 string            `json:"run_id"`
	PendingStatus         string            `json:"pending_status"`
	RunStatus             string            `json:"run_status"`
	Restricted            bool              `json:"restricted"`
	RoutineRevisionPinned bool              `json:"routine_revision_pinned"`
	StepOutputs           map[string]string `json:"step_outputs"`
}
