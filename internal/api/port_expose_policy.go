package api

import "context"

// PortExposeDecision is the outcome of evaluating a port-expose request
// against the active policy. MVP only emits ExposeAllow; the other values
// exist so a future approval layer can slot in without breaking the handler
// signature.
type PortExposeDecision string

const (
	// ExposeAllow — accept immediately, row is inserted as ACTIVE, URL is
	// returned to the agent synchronously. MVP default.
	ExposeAllow PortExposeDecision = "allow"

	// ExposePending — a future approval layer would mint a PENDING row and
	// block the sidecar on a long-poll until a human resolves it. Not used
	// by AllowAllPolicy.
	ExposePending PortExposeDecision = "pending"

	// ExposeDeny — policy says no. Handler returns 403 with the reason.
	ExposeDeny PortExposeDecision = "deny"
)

// PortExposeRequest captures the inputs a policy can discriminate on. Only
// the fields the policy sees are here; the handler holds the rest (tokens,
// container IPs, etc.) and shouldn't leak them into the decision.
type PortExposeRequest struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	AgentSlug   string
	Port        int
	Description string
	TTLSeconds  int
}

// PortExposePolicy decides whether to admit an expose request. The string
// return is a human-readable reason threaded through to the audit row and
// the agent's response so policy changes are observable.
type PortExposePolicy interface {
	Check(ctx context.Context, req *PortExposeRequest) (PortExposeDecision, string, error)
}

// AllowAllPolicy is the MVP policy: open by default. Every request is
// admitted immediately. The reason string is deliberately fixed so logs
// look consistent when grepping for which policy accepted a request.
type AllowAllPolicy struct{}

// Check always returns ExposeAllow. The ctx and req are ignored; kept for
// interface compatibility with future policies that will read agent role,
// port ranges, rate budgets, etc.
func (AllowAllPolicy) Check(_ context.Context, _ *PortExposeRequest) (PortExposeDecision, string, error) {
	return ExposeAllow, "open-by-default (MVP)", nil
}
