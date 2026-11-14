package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/craftapps"
)

// craftAppsGate applies the instance administrator's Word, Sheet and agent
// settings (#5315) to Craft requests. Turning an application off refuses new
// documents and new editing at once, lets open editors keep saving while it
// drains, and never touches a document: reads, release and reconcile always
// work. A nil gate (no policy configured, as in package tests) allows all.
type craftAppsGate struct {
	policy *craftapps.Policy
	db     *sql.DB
}

func newCraftAppsGate(policy *craftapps.Policy, db *sql.DB) *craftAppsGate {
	if policy == nil || db == nil {
		return nil
	}
	return &craftAppsGate{policy: policy, db: db}
}

func (g *craftAppsGate) mode(ctx context.Context, subject string) craftapps.Mode {
	if g == nil {
		return craftapps.ModeOpen
	}
	return g.policy.Mode(ctx, g.db, subject)
}

func (g *craftAppsGate) allows(ctx context.Context, application string, op craftapps.Operation) bool {
	return g.mode(ctx, application).Allows(op)
}

// agentsAllow requires both the application and the agents switch.
func (g *craftAppsGate) agentsAllow(ctx context.Context, application string, op craftapps.Operation) bool {
	return g.mode(ctx, craftapps.Agents).Allows(op) && (application == "" || g.allows(ctx, application, op))
}

// refuse writes the uniform refusal: 503 with a reason code the editor and
// the CLI show as "turned off by the instance administrator".
func (g *craftAppsGate) refuse(w http.ResponseWriter, ctx context.Context, subject string) {
	reason := craftapps.ReasonAppDisabled
	if g.mode(ctx, subject) == craftapps.ModeDraining {
		reason = craftapps.ReasonAppDraining
	}
	if subject == craftapps.Agents {
		reason = craftapps.ReasonAppDisabled
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error":       craftapps.Label(subject) + ": " + reason.Message(),
		"reason":      string(reason),
		"application": subject,
	})
}

// craftLeaseOperation classifies a lease mutation.
func craftLeaseOperation(operation string) craftapps.Operation {
	switch operation {
	case "acquire", "request", "handoff":
		return craftapps.OpStartEdit
	case "renew":
		return craftapps.OpContinue
	}
	return craftapps.OpAlways
}

// craftSnapshotOperation classifies a document mutation.
func craftSnapshotOperation(operation string) craftapps.Operation {
	if operation == "reconcile" {
		return craftapps.OpAlways
	}
	return craftapps.OpContinue
}

// craftAgentToolOperation classifies an agent's document operation.
func craftAgentToolOperation(operation string) craftapps.Operation {
	switch operation {
	case "acquire":
		return craftapps.OpStartEdit
	case "renew", "apply", "propose":
		return craftapps.OpContinue
	}
	return craftapps.OpAlways
}
