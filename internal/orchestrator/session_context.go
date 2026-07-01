package orchestrator

import "strings"

// sessionContextOpen / sessionContextClose delimit the per-turn volatile
// context prepended to the user message. Distinct, greppable markers so the
// model treats the block as background and downstream tooling can locate it.
const (
	sessionContextOpen  = "[SESSION CONTEXT]"
	sessionContextClose = "[END SESSION CONTEXT]"
)

// prependSessionContext frames the volatile per-turn context (conversation
// history, episodic recall, memory nudge, cost awareness) as a delimited block
// ahead of the user's actual message. This is the counterpart to keeping the
// system prompt stable: everything that changes turn-to-turn rides in the user
// turn instead of churning the cached system prefix.
//
// Returns the user message unchanged when there is no volatile context — the
// common case for fresh sessions and sub-agent runs (SkipConvHistory) — so
// those requests carry no empty wrapper.
func prependSessionContext(sessionCtx, userMessage string) string {
	sessionCtx = strings.TrimSpace(sessionCtx)
	if sessionCtx == "" {
		return userMessage
	}
	return sessionContextOpen + "\n" + sessionCtx + "\n" + sessionContextClose + "\n\n" + userMessage
}
