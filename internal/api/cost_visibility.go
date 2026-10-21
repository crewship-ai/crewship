package api

import (
	"context"
	"net/http"

	"github.com/crewship-ai/crewship/internal/costvis"
	"github.com/crewship-ai/crewship/internal/journal"
)

// canReadCosts is the one read rule for money on the wire (#3590): workspace
// OWNER and ADMIN. MANAGER, MEMBER, VIEWER, GUEST and an empty role do not
// read costs. Cost-only endpoints answer 403 (requireSpendReader); endpoints
// that mix costs with other data answer as usual with the cost fields left
// out (writeCostAwareJSON). The field list lives in internal/costvis.
func canReadCosts(ctx context.Context) bool {
	return canRole(RoleFromContext(ctx), "manage")
}

// requireSpendReader gates a cost-only endpoint: 403 for every role below
// ADMIN, after the workspace check.
func requireSpendReader(w http.ResponseWriter, r *http.Request) bool {
	if !canReadCosts(r.Context()) {
		replyError(w, http.StatusForbidden, "Spend requires OWNER or ADMIN")
		return false
	}
	return true
}

// writeCostAwareJSON is writeJSON for a response that mixes cost figures with
// data every member may read. OWNER/ADMIN get v unchanged; everyone else gets
// the same document without the costvis.Keys fields.
func writeCostAwareJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	if canReadCosts(r.Context()) {
		writeJSON(w, status, v)
		return
	}
	redacted, err := costvis.RedactValue(v)
	if err != nil {
		// Never fall back to the unredacted value.
		replyError(w, http.StatusInternalServerError, "encode response")
		return
	}
	writeJSON(w, status, redacted)
}

// costAwareEntries serializes journal entries for the caller: cost.incurred
// payloads, budget payloads, run totals and their formatted summaries are
// stripped below ADMIN.
func costAwareEntries(ctx context.Context, entries []journal.Entry) []map[string]any {
	rows := serializeEntries(entries)
	if canReadCosts(ctx) {
		return rows
	}
	for _, row := range rows {
		costvis.Redact(row)
	}
	return rows
}
