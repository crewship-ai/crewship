package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// What an agent's Sheet refusal means for the person who would attach the
// Sheet. The reason is the one the agent itself would be told; the detail is
// finer and only for the caller, who may read the Sheet.
const (
	craftSheetDetailWorkspaceFlag = "workspace_flag_off"
	craftSheetDetailChat          = "chat_not_private"
	craftSheetDetailNotSheet      = "not_a_sheet"
	craftSheetDetailNotOwner      = "not_owner"
	craftSheetDetailShared        = "not_in_personal_space"
	craftSheetDetailAgent         = "agent_unavailable"
	craftSheetDetailNoWrite       = "no_write_access"
	craftSheetDetailAgentsClosed  = "agents_" // + the administrator's mode reason
)

type craftNativeAgentAccessHolder struct {
	Kind string `json:"kind"`
	Mine bool   `json:"mine"`
}

// craftNativeAgentAccessResponse answers one question: if the caller attaches
// this Sheet to the next message in this chat, may the agent change it?
type craftNativeAgentAccessResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Holder is who edits the Sheet now, when anyone does. An agent's hold is
	// short and never a refusal here.
	Holder *craftNativeAgentAccessHolder `json:"holder,omitempty"`
	// MaxSheets is how many Sheets one message may attach for the agent to
	// change; with more, the agent may change none.
	MaxSheets int `json:"max_sheets"`
	// DefaultAllowed starts the consent switch on: allowed, and the Sheet came into this very chat
	// (uploaded or attached here). It permits nothing; consent is still the sent message.
	DefaultAllowed bool `json:"default_allowed"`
	// HeadVersion, SavedAt and SavedByName name the Sheet's latest confirmed
	// revision, the one an agent would start from, allowed or not. They are
	// given only to a caller who may read that revision (not to Read or
	// Comment grantees, who see the shared version only).
	HeadVersion string `json:"head_version,omitempty"`
	SavedAt     string `json:"saved_at,omitempty"`
	SavedByName string `json:"saved_by_name,omitempty"`
}

// agentAccess is advice for the UI, never a permission: every agent call checks
// everything again on its own.
func (m *craftNativeHTTP) agentAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !m.enabled || m.pages == nil || m.clock == nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	chat := r.URL.Query().Get("chat_id")
	if !mooring.ValidWireIdentifier(chat) {
		replyError(w, 400, "chat_id is required")
		return
	}
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	ws := WorkspaceIDFromContext(ctx)
	rec, err := m.pages.loadPageForRead(ctx, ws, r.PathValue("slug"))
	if err != nil {
		craftNativeHTTPError(w, errCraftNativeAccess)
		return
	}
	scope := mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: rec.ID}
	proof, err := m.pageAuthority(ctx, tx, source, scope, false)
	if err != nil {
		craftNativeHTTPError(w, errCraftNativeAccess)
		return
	}
	out := craftNativeAgentAccessResponse{MaxSheets: 1}
	if out.Holder, err = m.agentAccessHolder(ctx, tx, scope, source.UserID); err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	if proof.application != craftapps.Sheet {
		out.Reason, out.Detail = CraftSheetReasonNotAttached, craftSheetDetailNotSheet
	} else if out.Reason, out.Detail, err = m.agentAccessRefusal(ctx, tx, scope, rec, source.UserID, chat); err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	} else if out.Reason == "" && out.Holder != nil && out.Holder.Kind == "human" && !out.Holder.Mine {
		// Another person edits it now. The caller's own lease (they just opened the Sheet) does not
		// turn the chip off; the agent's acquire still answers holder_human
		// while that lease is held.
		out.Reason = CraftSheetReasonHolderHuman
	}
	out.Allowed = out.Reason == ""
	out.Message = craftSheetReasonText[out.Reason]
	if out.Allowed {
		// The chat is the caller's own private chat with the agent (checked above).
		var here int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifact_chat_refs WHERE chat_id=? AND page_id=? AND workspace_id=?)
 + (SELECT count(*) FROM artifact_message_refs WHERE chat_id=? AND page_id=? AND workspace_id=?)`, chat, rec.ID, ws, chat, rec.ID, ws).Scan(&here); err != nil {
			replyError(w, 503, "native Craft unavailable")
			return
		}
		out.DefaultAllowed = here > 0
	}
	if proof.application == craftapps.Sheet && proof.privateRead {
		if err = agentAccessHead(ctx, tx, scope, &out); err != nil {
			replyError(w, 503, "native Craft unavailable")
			return
		}
	}
	writeJSON(w, 200, out)
}

// agentAccessHead fills the latest confirmed revision: its number, when and by
// whom it was saved, named the way the version history names its author.
func agentAccessHead(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, out *craftNativeAgentAccessResponse) error {
	var version int64
	var actor, at string
	err := tx.QueryRowContext(ctx, `SELECT h.latest_version, v.actor, v.created_at FROM artifact_version_heads h
JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=h.latest_version
WHERE h.workspace_id=? AND h.page_id=? AND h.content_type='gridcraft'`, scope.WorkspaceID, scope.ArtifactID).Scan(&version, &actor, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	name := "Crewship"
	switch {
	case strings.HasPrefix(actor, "agent:"):
		name = "Agent"
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(name,''),'Agent') FROM agents WHERE id=? AND workspace_id=?`, strings.TrimPrefix(actor, "agent:"), scope.WorkspaceID).Scan(&name)
	case strings.HasPrefix(actor, "user:"):
		name = "User"
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(full_name,''),'User') FROM users WHERE id=?`, strings.TrimPrefix(actor, "user:")).Scan(&name)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	out.HeadVersion, out.SavedAt, out.SavedByName = strconv.FormatInt(version, 10), at, name
	return nil
}

// agentAccessRefusal follows a chat run's own chain in its order: the server,
// the message's admission, the run's binding. Nothing here may be wider than
// attachChatRunEntry; TestChatRunSheetScenarioWhoMayChangeTheSheet compares both.
func (m *craftNativeHTTP) agentAccessRefusal(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, rec *pageRecord, user, chat string) (reason, detail string, err error) {
	if state, why := m.agents.ChatRunReadiness(); state != craftapps.StateReady {
		return CraftSheetReasonEngineFailed, string(why), nil
	}
	if !m.pages.artifactRunModesOn(ctx, scope.WorkspaceID) {
		return CraftSheetReasonFeatureOff, craftSheetDetailWorkspaceFlag, nil
	}
	// The person's own private chat with one agent. A chat that is someone
	// else's, shared, or missing reads the same.
	receipt, err := chataudience.CaptureTrustedQuery(ctx, tx, chat, user, nil)
	if err != nil {
		return "", "", err
	}
	if receipt == nil || receipt.WorkspaceID != scope.WorkspaceID {
		return CraftSheetReasonNotAttached, craftSheetDetailChat, nil
	}
	var own int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND agent_id=? AND visibility='private'`, chat, scope.WorkspaceID, user, receipt.AgentID).Scan(&own); err != nil {
		return "", "", err
	}
	if own != 1 {
		return CraftSheetReasonNotAttached, craftSheetDetailChat, nil
	}
	viewer, err := m.pages.delegableViewer(ctx, scope.WorkspaceID, user)
	if err != nil {
		return "", "", err
	}
	if ok, err := m.pages.delegableRead(ctx, scope.WorkspaceID, rec.ID, viewer); err != nil {
		return "", "", err
	} else if !ok {
		return CraftSheetReasonNotAttached, craftSheetDetailNotOwner, nil
	}
	var crew string
	err = tx.QueryRowContext(ctx, `SELECT a.crew_id FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL`, receipt.AgentID, scope.WorkspaceID).Scan(&crew)
	if errors.Is(err, sql.ErrNoRows) {
		return CraftSheetReasonNotAttached, craftSheetDetailAgent, nil
	}
	if err != nil {
		return "", "", err
	}
	if !m.pages.artifactAgentPolicyAllows(ctx, scope.WorkspaceID, crew) {
		return CraftSheetReasonAgentPolicy, "", nil
	}
	source, err := craftNativeHuman(ctx)
	if err != nil {
		return "", "", err
	}
	switch {
	case rec.OwnerUserID != user || rec.OwnerCrewID != "":
		return CraftSheetReasonNotAttached, craftSheetDetailNotOwner, nil
	case !craftNativePersonalSpace(rec, user):
		return CraftSheetReasonNotAttached, craftSheetDetailShared, nil
	}
	if _, err = m.pageAuthority(ctx, tx, source, scope, true); err != nil || !m.pages.humanAgentMayWrite(ctx, scope.WorkspaceID, rec.ID, user, receipt.AgentID) {
		return CraftSheetReasonNotAttached, craftSheetDetailNoWrite, nil
	}
	for _, op := range []craftapps.Operation{craftAgentToolOperation("acquire"), craftAgentToolOperation("apply")} {
		if !m.apps.mode(ctx, craftapps.Agents).Allows(op) {
			return CraftSheetReasonEngineFailed, craftSheetDetailAgentsClosed + string(m.apps.reason(ctx, craftapps.Agents)), nil
		}
		if !m.apps.allows(ctx, craftapps.Sheet, op) {
			return CraftSheetReasonEngineFailed, string(m.apps.reason(ctx, craftapps.Sheet)), nil
		}
	}
	return "", "", nil
}

// agentAccessHolder reads the Sheet's live edit lease, or nil.
func (m *craftNativeHTTP) agentAccessHolder(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, user string) (*craftNativeAgentAccessHolder, error) {
	var actor, session, boot string
	var expires int64
	err := tx.QueryRowContext(ctx, `SELECT actor_id,session_id,boot_id,expires_ns FROM mooring_edit_leases WHERE page_id=? AND workspace_id=?`, scope.ArtifactID, scope.WorkspaceID).Scan(&actor, &session, &boot, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	currentBoot, now, err := m.clock.Now()
	if err != nil {
		return nil, err
	}
	if actor == "" || session == "" || boot != currentBoot || int64(now) >= expires {
		return nil, nil
	}
	if strings.HasPrefix(actor, "agent:") {
		return &craftNativeAgentAccessHolder{Kind: "agent"}, nil
	}
	return &craftNativeAgentAccessHolder{Kind: "human", Mine: actor == "user:"+user}, nil
}
