package api

// POST /api/v1/onboarding/setup-agent/start — the route
// `components/features/onboarding/setup-agent-api.ts`'s `startSetupAgentSession`
// has always called and `ensureOnboardingSetupCrew` (onboarding_setup_crew.go)
// has always been able to serve. Neither side was wired to the other: this
// file is the wire.
//
// SEQUENCING DECISION — read this before reordering steps in
// app/(onboarding)/onboarding/page.tsx to "fix" this differently.
//
// The premise the frontend's stub comment gave for why this endpoint
// couldn't exist yet was wrong: it isn't that "this wizard does not create
// the workspace until step 3's Launch". Every workspace is created
// synchronously at signup/bootstrap (auth.go's Signup and Bootstrap handlers
// both INSERT INTO workspaces before the client ever sees the wizard);
// onboarding.Setup only ever UPDATEs the one that already exists
// (persistOnboardingPrefs, setupFromTemplate's workspace-name UPDATE). So a
// workspace id — and therefore a target for ensureOnboardingSetupCrew — is
// available from step 1 onward, same as it already is for GET
// /onboarding/status's own best-effort call.
//
// The real blocker is narrower: the wizard COLLECTS the model credential in
// step 3, but onboarding.Setup only PERSISTS it — as a row in `credentials`
// — at the final Launch submission. Before that click, apiKey lives in a
// React useState and nothing before it ever calls the database. So simply
// moving step 3's JSX earlier in the page would not fix anything: the setup
// agent's container would still boot with no credential to authenticate
// with, because there is still no write anywhere before Launch to give it
// one. Making an early write real would mean adding a new mid-wizard
// database call (e.g. step 1 or 2 starts POSTing to /api/v1/credentials
// directly) AND then teaching setupFromTemplate's own credential insert
// (insertOnboardingCredential) not to insert a SECOND row for the same
// value when Launch runs — a change to the onboarding write path's
// idempotency contract, and its own body of work, not a one-file fix for
// this integration gap.
//
// So this endpoint takes this task's other offered branch: refuse with a
// reason the frontend can act on, rather than open a chat the agent can
// never answer in. workspaceHasCredential is the same check GET
// /onboarding/status already uses to gate CREATING the setup crew at all;
// this reuses it as a precondition on OPENING the chat, replying 428
// Precondition Required with a machine-readable `reason` field so the
// frontend can tell "no credential yet" apart from a genuine outage and say
// so — see OnboardingSetupChat's `onUnavailable` handling in
// onboarding-setup-chat.tsx and page.tsx's handleSetupAgentUnavailable. The
// one outcome this rules out, per this task's brief, is a chat box that
// silently never answers: a workspace with no credential gets an explicit,
// distinguishable refusal instead of a crew, agent and chat it could never
// get an answer out of.
import (
	"net/http"
)

// onboardingSetupAgentStartResponse is what a successful Start answers with
// — exactly the two fields setup-agent-api.ts's startSetupAgentSession
// parses (`agent_id`, `session_id`). SessionID is the setup agent's chat id:
// `chats.id` is the same identifier the wire protocol and
// conversation_messages.session_id call "session_id" throughout this
// codebase (see agent_chats.go's CreateChat), so no translation happens
// here beyond the field rename.
type onboardingSetupAgentStartResponse struct {
	AgentID     string `json:"agent_id"`
	SessionID   string `json:"session_id"`
	WorkspaceID string `json:"workspace_id"`
}

// StartSetupAgent handles POST /api/v1/onboarding/setup-agent/start.
//
// Idempotent by construction: it does no writing of its own beyond calling
// ensureOnboardingSetupCrew, which already finds-or-inserts the crew, agent
// and chat by their fixed unique keys (see that function's own doc comment
// and TestEnsureOnboardingSetupCrew_SecondCallConvergesOnSameRows). A second
// call — a page refresh, a remounted chat pane — returns the same agent_id
// and session_id rather than standing up a second crew.
func (h *OnboardingHandler) StartSetupAgent(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	wsID, ok := h.firstWorkspaceID(r.Context(), user.ID)
	if !ok {
		replyError(w, http.StatusBadRequest, "No workspace found for user")
		return
	}

	// The precondition this whole file exists to enforce — see the
	// SEQUENCING DECISION above. Refuse BEFORE calling
	// ensureOnboardingSetupCrew: a workspace with no credential yet gets no
	// crew/agent/chat rows at all, not a set of rows for an agent that could
	// never have answered.
	if !h.workspaceHasCredential(r.Context(), wsID) {
		writeJSON(w, http.StatusPreconditionRequired, map[string]string{
			"error":  "Add a model token before starting the setup agent — it runs in a container and cannot answer without one.",
			"reason": "credential_required",
		})
		return
	}

	info, err := ensureOnboardingSetupCrew(r.Context(), h.db, h.logger, wsID, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "start setup agent", err)
		return
	}

	writeJSON(w, http.StatusOK, onboardingSetupAgentStartResponse{
		AgentID:     info.AgentID,
		SessionID:   info.ChatID,
		WorkspaceID: wsID,
	})
}
