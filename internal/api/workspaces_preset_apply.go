package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

type presetApplyRequest struct {
	Selection access.PresetRequest `json:"selection"`
	Digest    string               `json:"confirmation_digest"`
}

// ApplyAccessPreset recomputes consent against live state in the same writer
// transaction as canonical grants, audit and the final informational selection.
func (h *WorkspaceHandler) ApplyAccessPreset(w http.ResponseWriter, r *http.Request) {
	actor, user, workspace, ok := h.memberAccessTarget(w, r)
	if !ok {
		return
	}
	var input presetApplyRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || input.Selection.MembershipID != r.PathValue("memberId") || input.Selection.Revision < 1 {
		replyError(w, 400, "Provide an explicit selection and its preview confirmation_digest")
		return
	}
	if decoded, err := hex.DecodeString(input.Digest); err != nil || len(decoded) != 32 {
		replyError(w, 400, "Provide the preview confirmation_digest")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	defer tx.Rollback()
	store := access.Store{DB: h.db}
	preview, err := store.PlanPresetIn(r.Context(), tx, actor, user, workspace, input.Selection)
	if errors.Is(err, access.ErrPresetInvalid) {
		replyError(w, 400, "Invalid preset or reissue selection; target must already be restricted")
		return
	}
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	if preview.Digest != input.Digest {
		replyError(w, 409, "Member authority or preview consequences changed; request and confirm a fresh preview")
		return
	}
	before, err := store.PolicyIn(r.Context(), tx, actor, user, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	afterMembership, err := store.ReplacePresetIn(r.Context(), tx, actor, user, workspace, preview.Selection)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	after, err := store.PolicyIn(r.Context(), tx, actor, user, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	entry := journal.Entry{WorkspaceID: workspace, Type: journal.EntryMemberAccessChanged, ActorType: journal.ActorUser, ActorID: actor, Summary: "Member resource preset applied", Payload: map[string]any{
		"member_id": after.ID, "subject_user_id": user, "previous_mode": before.Mode, "mode": after.Mode,
		"previous_revision": before.Revision, "revision": after.Revision,
		"previous_grants": auditGrantDetails(before.GrantDetails), "grants": auditGrantDetails(after.GrantDetails), "reissued_rights": preview.Selection.Reissue,
		"preset_id": preview.Selection.PresetID, "preset_version": preview.Selection.Version, "preset_level": preview.Selection.Level, "definition_hash": preview.DefinitionHash,
	}}
	appendTx := journal.AppendTx
	if h.policyJournal != nil {
		emitter, ok := h.policyJournal.(journal.TransactionEmitter)
		if !ok {
			h.memberAccessError(w, errors.New("member access journal does not support atomic writes"))
			return
		}
		appendTx = emitter.AppendTx
	}
	notify, err := appendTx(r.Context(), tx, entry)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	if err = store.RecordPresetSelectionIn(r.Context(), tx, actor, user, workspace, afterMembership, preview.Selection); err != nil {
		h.memberAccessError(w, err)
		return
	}
	after, err = store.PolicyIn(r.Context(), tx, actor, user, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.memberAccessError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	h.writeMemberAccessPolicy(w, r, after)
}
