package api

// Response envelopes for the routines workspace read surfaces. These exist so
// cmd/gen-openapi's components can be paired against a real struct in
// responseShapeContracts: the pair derives `required` from the json tags, so
// an envelope built from a map literal can never be checked. The field names
// and their emit-always/omit-empty split are exactly what the handlers wrote
// before, so the wire is unchanged.

type routineCalendarEvent struct {
	Inputs        *map[string]any `json:"inputs,omitempty"`
	PinnedVersion *int            `json:"pinned_version,omitempty"`
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	At            string          `json:"at"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	// Planned occurrences carry the schedule they came from; recorded runs
	// carry their result. Neither kind emitted the other's fields before.
	ScheduleID string `json:"schedule_id,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	Status     string `json:"status,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
}

type routineCalendarResponse struct {
	Events    []routineCalendarEvent `json:"events"`
	Truncated bool                   `json:"truncated"`
}

type pipelineRunStepExecution struct {
	ID                string `json:"id"`
	ParentExecutionID string `json:"parent_execution_id"`
	StepID            string `json:"step_id"`
	ExecutionPath     string `json:"execution_path"`
	Attempt           int    `json:"attempt"`
	Kind              string `json:"kind"`
	Status            string `json:"status"`
	AgentSlug         string `json:"agent_slug"`
	Model             string `json:"model"`
	StartedAt         string `json:"started_at"`
	EndedAt           string `json:"ended_at"`
	Error             string `json:"error"`
	OutputBytes       int    `json:"output_bytes"`
}

type pipelineRunStepExecutionList struct {
	Rows []pipelineRunStepExecution `json:"rows"`
	// nil renders as null, which is what the map literal did with an unset
	// `any` — absence of a cursor is a value here, not a missing field.
	NextCursor *string `json:"next_cursor"`
}

type pipelineRunStepExecutionOutput struct {
	ID     string `json:"id"`
	Output string `json:"output"`
}

type pipelineRunArtifact struct {
	ID              string `json:"id"`
	StepExecutionID string `json:"step_execution_id"`
	Kind            string `json:"kind"`
	Label           string `json:"label"`
	State           string `json:"state"`
	MediaType       string `json:"media_type"`
	SHA256          string `json:"sha256"`
	ContentBytes    int    `json:"content_bytes"`
	Source          string `json:"source"`
	Error           string `json:"error"`
	CreatedAt       string `json:"created_at"`
	ExecutionPath   string `json:"execution_path"`
	Attempt         int    `json:"attempt"`
}

type pipelineRunArtifactList struct {
	Artifacts  []pipelineRunArtifact `json:"artifacts"`
	Truncated  bool                  `json:"truncated"`
	NextCursor *string               `json:"next_cursor"`
}

type pipelineRunArtifactContent struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}
