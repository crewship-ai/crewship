package bedrock

// ResolvePath is an internal sidecar-only surface, never an agent tool.
const ResolvePath = "/api/v1/internal/bedrock/resolve"

type ResolveRequest struct {
	RunID     string `json:"run_id"`
	AgentID   string `json:"agent_id"`
	AccountID string `json:"account_id"`
	Model     string `json:"model"`
}

type ResolveResponse struct {
	AccountID string  `json:"account_id"`
	Account   Account `json:"account"`
}
