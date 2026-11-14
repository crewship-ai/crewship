package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Agent access modes, enforcement (D1, #5317 PR2).
//
// With the workspace flag on, a chat run no longer needs an exact-run consent
// bound to one page. It reaches an artifact in one of three ways, each checked
// on every use:
//
//   - referenced: the human attached it to the message that started the run
//     (artifact_run_scope, written only by trusted human-message admission);
//   - created: the run created it in this chat, and the human still owns it;
//   - standing: the agent holds its own live grant on it (amendment 1).
//
// Whatever the way, the human must still be able to read the artifact as an
// ordinary member (delegableViewer), so access held only through the ADMIN or
// OWNER role never passes to the agent (amendment 2). Anything else is refused
// exactly like a missing artifact (amendment 5). Writes stay narrower: only an
// artifact the human owns, attached or created in this run. With the flag off
// none of this runs and the consent path is unchanged.

// Defaults for a workspace without a policy row (and the column defaults).
const (
	defaultChatRunCreateLimit  = 3
	defaultRunReadLimit        = 50
	defaultCrewHourlyPageLimit = 20
)

// artifactRunLimits are the per-workspace caps, read on every use.
type artifactRunLimits struct {
	ChatRunCreate  int `json:"chat_run_create_limit"`
	RunRead        int `json:"run_read_limit"`
	CrewHourlyPage int `json:"crew_hourly_page_limit"`
}

func loadArtifactRunLimits(ctx context.Context, q pageAuthQueryer, workspace string) (artifactRunLimits, error) {
	l := artifactRunLimits{defaultChatRunCreateLimit, defaultRunReadLimit, defaultCrewHourlyPageLimit}
	err := q.QueryRowContext(ctx, `SELECT chat_run_create_limit,run_read_limit,crew_hourly_page_limit FROM workspace_artifact_creation_policy WHERE workspace_id=?`, workspace).Scan(&l.ChatRunCreate, &l.RunRead, &l.CrewHourlyPage)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	return l, err
}

// artifactRunModesOn reports the workspace flag through the caller's reader,
// so a check inside a writer transaction sees the same snapshot.
func (h *PageHandler) artifactRunModesOn(ctx context.Context, workspace string) bool {
	if workspace == "" {
		return false
	}
	on, err := featureflags.IsEnabledWith(ctx, h.pageAuthReader(ctx), workspace, ArtifactAgentAccessModesFlag)
	return err == nil && on
}

// artifactRunModesRun returns the run when the request is an agent run in a
// workspace with the flag on, and nil otherwise.
func (h *PageHandler) artifactRunModesRun(ctx context.Context) *artifactRunAuthority {
	a, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	if a == nil || !h.artifactRunModesOn(ctx, a.WorkspaceID) {
		return nil
	}
	return a
}

// checkScopedArtifactRun is the run-level gate with the flag on. It replaces
// the exact-run consent and keeps everything else: a live, unchanged human
// principal, the workspace policy ceiling, the original credential, and the
// human's own private chat with this agent.
func (h *PageHandler) checkScopedArtifactRun(ctx context.Context, a *artifactRunAuthority, scope string) error {
	if a == nil || !h.artifactRunFamilyEnabled(ctx, a.WorkspaceID, "") {
		return errArtifactProjectAccess
	}
	if h.checkOriginalUserArtifactRunPrincipal(ctx, a) != nil || !h.artifactScopedPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return errArtifactProjectAccess
	}
	var own int
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT count(*) FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND agent_id=? AND visibility='private'`,
		a.ChatID, a.WorkspaceID, a.HumanUserID, a.AgentID).Scan(&own); err != nil || own != 1 {
		return errArtifactProjectAccess
	}
	if _, err := h.validateArtifactPrincipalOriginalCredential(ctx, a, scope); err != nil {
		return errArtifactProjectAccess
	}
	return nil
}

const (
	runReachNone       = ""
	runReachReferenced = "referenced"
	runReachCreated    = "created"
	runReachStanding   = "standing"
	// The two below behave like referenced and standing; they are kept apart
	// only so the audit names why the run reached the artifact.
	runReachConsented = "consented"
	runReachFolder    = "folder"
)

// readOnlyReach: standing and folder reach never write.
func readOnlyReach(reach string) bool { return reach == runReachStanding || reach == runReachFolder }

// artifactRunReach says how the run reaches the page, without writing.
// standingNew is true when the standing path would count a new distinct read.
func (h *PageHandler) artifactRunReach(ctx context.Context, a *artifactRunAuthority, rec *pageRecord) (reach string, standingNew bool, err error) {
	q := h.pageAuthReader(ctx)
	var n int
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM artifact_run_scope WHERE run_id=? AND workspace_id=? AND page_id=?`, a.RunID, a.WorkspaceID, rec.ID).Scan(&n); err != nil {
		return
	}
	if n == 1 {
		return runReachReferenced, false, nil
	}
	// The exact-run consent of today (an explicit "edit with agent" or a
	// comment task) keeps its one page in reach, so turning the flag on never
	// takes away what the human deliberately granted to this run.
	if bound, e := h.artifactProjectRunPage(ctx, a); e == nil && bound.Valid && bound.String == rec.ID && h.checkArtifactProjectRunConsent(ctx, a, rec.ID) == nil {
		return runReachConsented, false, nil
	}
	// Created in this chat by this agent and still owned by the human
	// (amendment 4): it stays in reach for later messages of the chat.
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM artifact_project_origins o JOIN pages p ON p.id=o.page_id AND p.workspace_id=o.workspace_id AND p.owner_user_id=o.human_user_id AND p.owner_crew_id IS NULL
WHERE o.page_id=? AND o.workspace_id=? AND o.human_user_id=? AND o.chat_id=? AND o.agent_id=?`, rec.ID, a.WorkspaceID, a.HumanUserID, a.ChatID, a.AgentID).Scan(&n); err != nil {
		return
	}
	if n == 1 {
		return runReachCreated, false, nil
	}
	grants, err := h.agentGrants(ctx, a.WorkspaceID, rec, a.AgentID)
	if err != nil {
		return runReachNone, false, err
	}
	readable, standing := false, runReachStanding
	for _, g := range grants {
		switch g.Level {
		case pageGrantRead, pageGrantPropose, pageGrantWrite:
			readable = true
		}
	}
	if !readable {
		// D2 (#5317 PR3): a folder may let the agents of its owning crew read
		// (or suggest on) the non-private pages filed in it. The agent's live
		// crew must be the folder's owner; a folder never gives write.
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM pages p JOIN page_folders f ON f.id=p.folder_id AND f.workspace_id=p.workspace_id
JOIN agents a ON a.id=? AND a.workspace_id=p.workspace_id AND a.deleted_at IS NULL AND a.crew_id=f.owner_crew_id
JOIN crews c ON c.id=f.owner_crew_id AND c.workspace_id=p.workspace_id AND c.deleted_at IS NULL
WHERE p.id=? AND p.workspace_id=? AND p.private_audience=0 AND f.agent_access IN ('read','propose')`, a.AgentID, rec.ID, a.WorkspaceID).Scan(&n); err != nil {
			return
		}
		if n != 1 || a.CrewID == "" {
			return runReachNone, false, nil
		}
		var crew string
		if err = q.QueryRowContext(ctx, `SELECT coalesce(crew_id,'') FROM agents WHERE id=? AND workspace_id=?`, a.AgentID, a.WorkspaceID).Scan(&crew); err != nil || crew != a.CrewID {
			return runReachNone, false, err
		}
		standing = runReachFolder
	}
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM artifact_run_access WHERE run_id=? AND page_id=? AND source='standing'`, a.RunID, rec.ID).Scan(&n); err != nil {
		return
	}
	return standing, n == 0, nil
}

// errArtifactRunCap refuses past a per-run limit. It is only returned once the
// run already reaches the artifact, so it reveals nothing about existence.
var errArtifactRunCap = errors.New("artifact run limit reached")

// authorizeScopedArtifactRun decides a flag-on run's access to one artifact,
// inside the operation's own transaction. It returns the run's actor label.
func (h *PageHandler) authorizeScopedArtifactRun(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, workspace string, rec *pageRecord, write, share bool) (string, error) {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if a.WorkspaceID != workspace || rec == nil || rec.ArchivedAt != "" {
		return "", errArtifactRefNotFound
	}
	// The run-level gate is the same for every slug; the write scope is
	// checked only once the run reaches the artifact, so its refusal says
	// nothing about artifacts the run does not reach.
	if err := h.checkScopedArtifactRun(ctx, a, "pages:read"); err != nil {
		return "", err
	}
	reach, standingNew, err := h.artifactRunReach(ctx, a, rec)
	if err != nil {
		return "", err
	}
	if reach == runReachNone {
		return "", errArtifactRefNotFound
	}
	viewer, err := h.delegableViewer(ctx, workspace, a.HumanUserID)
	if err != nil {
		return "", err
	}
	if ok, err := h.delegableRead(ctx, workspace, rec.ID, viewer); err != nil {
		return "", err
	} else if !ok {
		return "", errArtifactRefNotFound
	}
	// An agent never shares, grants, moves or publishes (spec §2).
	if share {
		return "", errArtifactProjectAccess
	}
	if write {
		if _, err := h.validateArtifactPrincipalOriginalCredential(ctx, a, "pages:write"); err != nil {
			return "", errArtifactProjectAccess
		}
	}
	if write && (readOnlyReach(reach) || rec.OwnerUserID != a.HumanUserID || rec.OwnerCrewID != "") {
		return "", errArtifactProjectAccess
	}
	if readOnlyReach(reach) && standingNew {
		limits, err := loadArtifactRunLimits(ctx, tx, workspace)
		if err != nil {
			return "", err
		}
		var used int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_run_access WHERE run_id=? AND source='standing'`, a.RunID).Scan(&used); err != nil {
			return "", err
		}
		if used >= limits.RunRead {
			return "", errArtifactRunCap
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_run_access(run_id,workspace_id,page_id,source,created_at) VALUES(?,?,?,'standing',?) ON CONFLICT(run_id,page_id) DO NOTHING`,
			a.RunID, workspace, rec.ID, tsformat.Format(time.Now().UTC())); err != nil {
			return "", err
		}
	}
	operation := "read"
	if write {
		operation = "write"
	}
	if err := recordAgentRunArtifactAccess(ctx, tx, a, rec.ID, reach, operation); err != nil {
		return "", err
	}
	return "agent:" + a.AgentID + ":" + a.RunID, nil
}

// errArtifactRefNotFound is how a flag-on run is refused an artifact it does
// not reach: the very error a missing artifact gets.
var errArtifactRefNotFound = artifacts.ErrVersionNotFound

// reserveScopedArtifactRunCreate checks the per-run create cap and records the
// new artifact as created by this run, in the create's own transaction.
func (h *PageHandler) reserveScopedArtifactRunCreate(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, workspace, pageID string) error {
	limits, err := loadArtifactRunLimits(ctx, tx, workspace)
	if err != nil {
		return err
	}
	var used int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_run_access WHERE run_id=? AND source='created'`, a.RunID).Scan(&used); err != nil {
		return err
	}
	if used >= limits.ChatRunCreate {
		return errArtifactRunCap
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_run_access(run_id,workspace_id,page_id,source,created_at) VALUES(?,?,?,'created',?)`,
		a.RunID, workspace, pageID, tsformat.Format(time.Now().UTC())); err != nil {
		return err
	}
	return recordAgentRunArtifactAccess(ctx, tx, a, pageID, runReachCreated, "create")
}

// crewHourlyPageCapReached counts the crew's pages created in the last hour
// that no person authored (agent, routine and onboarding saves alike),
// inside the caller's transaction, with the flag on only.
func (h *PageHandler) crewHourlyPageCapReached(ctx context.Context, tx *sql.Tx, workspace, crew string) (bool, int, error) {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if crew == "" || !h.artifactRunModesOn(ctx, workspace) {
		return false, 0, nil
	}
	limits, err := loadArtifactRunLimits(ctx, tx, workspace)
	if err != nil {
		return false, 0, err
	}
	since := h.evaluator().Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pages p WHERE p.workspace_id=? AND p.owner_crew_id=? AND julianday(p.created_at) >= julianday(?) AND NOT EXISTS(SELECT 1 FROM page_versions v WHERE v.page_id=p.id AND v.seq=1 AND v.author_user_id IS NOT NULL)`, workspace, crew, since).Scan(&n); err != nil {
		return false, 0, err
	}
	return n >= limits.CrewHourlyPage, limits.CrewHourlyPage, nil
}

// auditActionAgentRunArtifactAccess records an agent run's access to an
// artifact on behalf of its human (#5317 PR4).
const auditActionAgentRunArtifactAccess = "artifact.agent_run_accessed"

// artifactAuditProbeKey marks a write-authority check that only shapes a read
// response (may the caller edit?); it is not an access and is not audited.
type artifactAuditProbeKey struct{}

// artifactAuditProbe marks an authorization that checks access without
// performing it (a pre-check before the write's own transaction, or a
// capability probe): it records no agent-run audit row.
func artifactAuditProbe(authorize artifacts.VersionAuthorize) artifacts.VersionAuthorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		return authorize(context.WithValue(ctx, artifactAuditProbeKey{}, true), tx)
	}
}

// recordAgentRunArtifactAccess writes the audit of one flag-on run access in
// the access's own transaction. The human the agent acted for is the row's
// user; the record is content-free. A read and a write authorization are each
// recorded once per run and artifact (each committed version already names
// the run as its actor in the version history); a create every time. The row stays in audit_logs only:
// the audit listing applies the private-artifact audience filter, the journal
// has none. Any failure, including a silently dropped insert, refuses the
// access.
func recordAgentRunArtifactAccess(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, pageID, reach, operation string) error {
	meta, err := json.Marshal(map[string]string{"agent_id": a.AgentID, "crew_id": a.CrewID, "run_id": a.RunID, "chat_id": a.ChatID, "reach": reach, "operation": operation})
	if err != nil {
		return err
	}
	if probe, _ := ctx.Value(artifactAuditProbeKey{}).(bool); probe {
		return nil
	}
	id := generateCUID()
	if operation != "create" {
		sum := sha256.Sum256([]byte(a.RunID + "\x00" + pageID + "\x00" + operation))
		id = "agent-run-" + operation + "-" + hex.EncodeToString(sum[:16])
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,'page',?,?,?) ON CONFLICT(id) DO NOTHING`,
		id, a.WorkspaceID, a.HumanUserID, auditActionAgentRunArtifactAccess, pageID, string(meta), tsformat.Format(time.Now().UTC())); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE id=? AND workspace_id=? AND user_id=? AND action=? AND entity_id=?`, id, a.WorkspaceID, a.HumanUserID, auditActionAgentRunArtifactAccess, pageID).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errors.New("agent run artifact audit unavailable")
	}
	return nil
}
