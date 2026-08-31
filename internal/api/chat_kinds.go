package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/chatkind"
)

// The chat-kind partition, as this package uses it.
//
// The rule itself moved to internal/chatkind when a SECOND surface needed it:
// internal/chatnotify decides by the same partition whether an assistant reply
// earns a place in somebody's inbox, and a private copy there would let the
// conversations column hide a routine step while the bell announced it. A
// vocabulary two surfaces disagree about is worse than no vocabulary.
//
// What stays here are aliases, so every caller and test in this package keeps
// its spelling — `ChatKindOf`, `ChatKindRoutine` — and so the handlers below
// read as API code rather than as a tour of another package.

type ChatKind = chatkind.Kind

const (
	ChatKindDirect  = chatkind.Direct
	ChatKindRoutine = chatkind.Routine
	ChatKindIssue   = chatkind.Issue
	ChatKindAgent   = chatkind.Agent
)

// ChatOriginValues is the whitelist every chat-creating endpoint shares.
var ChatOriginValues = chatkind.OriginValues

// IsChatOrigin reports whether v is a storable origin.
func IsChatOrigin(v string) bool { return chatkind.IsOrigin(v) }

// ChatKindOf classifies one row the same way the SQL does.
func ChatKindOf(mode, origin string) ChatKind { return chatkind.Of(mode, origin) }

// AllChatKinds is the vocabulary, in the order a UI should offer it.
var AllChatKinds = chatkind.All

// ChatKindCountsHeader carries the per-kind totals for the agent the request
// named.
const ChatKindCountsHeader = chatkind.CountsHeader

// chatKindPredicates maps a kind onto its WHERE fragment over `chats c`.
var chatKindPredicates = chatkind.Predicates

// parseChatKinds turns the `kind` query parameter into a WHERE fragment.
func parseChatKinds(raw string) (string, error) { return chatkind.ParseFilter(raw) }

// formatChatKindCounts renders the counts header value.
func formatChatKindCounts(counts map[ChatKind]int) string { return chatkind.FormatCounts(counts) }

// chatKindCounts totals an agent's chats per kind.
//
// It groups on (mode, origin) in SQL and folds the groups through
// chatkind.Of in Go, rather than running one COUNT per kind predicate. That is
// not an optimisation — it is what makes the totals incapable of disagreeing
// with the page beside them. A second SQL copy of the partition is a second
// thing to keep in step with the first, and the number on a bucket would be
// the last place anybody noticed it had drifted.
//
// (mode, origin) has a dozen distinct values at most, so the grouping is an
// index scan of the agent's rows with no temp b-tree worth worrying about.
func (h *AgentHandler) chatKindCounts(ctx context.Context, agentID, workspaceID string) (map[ChatKind]int, error) {
	return chatkind.CountByAgent(ctx, h.db, agentID, workspaceID)
}
