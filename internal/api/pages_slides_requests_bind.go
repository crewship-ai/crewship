package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/slides"
)

func (pages *PageHandler) bindSelectedSlidesRun(r *http.Request, id, run string, expected *chataudience.Receipt) error {
	if expected == nil || pages.preStartupSelectedRequest(id) || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		return errSlideAccess
	}
	h := &slideHTTP{pages: pages, store: pages.newSlidesStore()}
	var key slides.Key
	if pages.db.QueryRowContext(r.Context(), `SELECT workspace_id,page_id FROM slides_requests WHERE id=?`, id).Scan(&key.WorkspaceID, &key.PageID) != nil {
		return errSlideAccess
	}
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		record, err := slides.ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil || record.Summary.Status != "running" || record.Summary.Restored || record.Admission.AudienceJSON != mustArtifactReceiptJSON(expected) {
			return "", errSlideAccess
		}
		if _, err = h.selectedAdmissionLive(ctx, tx, key, record.Admission); err != nil {
			return "", errSlideAccess
		}
		var agent, crew, human, chat, origin string
		if tx.QueryRowContext(ctx, `SELECT agent_id,crew_id,human_user_id,chat_id,COALESCE(slides_request_origin,'') FROM artifact_run_principals WHERE run_id=? AND workspace_id=?`, run, key.WorkspaceID).Scan(&agent, &crew, &human, &chat, &origin) != nil || origin != id || human != record.Admission.HumanUserID || agent != record.Admission.AgentID || chat != record.Admission.ChatID {
			return "", errSlideAccess
		}
		a := &artifactRunAuthority{WorkspaceID: key.WorkspaceID, AgentID: agent, CrewID: crew, RunID: run, HumanUserID: human, ChatID: chat}
		if pages.checkArtifactRunPrincipal(ctx, a) != nil {
			return "", errSlideAccess
		}
		var other bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM document_requests WHERE run_id=?) OR EXISTS(SELECT 1 FROM page_artifact_run_delegations WHERE run_id=? AND (request_id IS NOT NULL OR document_request_id IS NOT NULL OR (slides_request_id IS NOT NULL AND slides_request_id<>?)))`, run, run, id).Scan(&other) != nil || other {
			return "", errSlideAccess
		}
		return record.Summary.HumanActor, nil
	}
	return h.store.BindSelectedRun(r.Context(), key, id, run, authorize, func(ctx context.Context, tx *sql.Tx, record slides.SelectedAuthorityState) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at,slides_request_id) SELECT ?,run_id,human_user_id,expires_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'),? FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND slides_request_origin=? AND chat_id=? AND agent_id=? AND human_user_id=? ON CONFLICT(page_id,run_id) DO UPDATE SET slides_request_id=excluded.slides_request_id WHERE page_artifact_run_delegations.request_id IS NULL AND page_artifact_run_delegations.document_request_id IS NULL AND (page_artifact_run_delegations.slides_request_id IS NULL OR page_artifact_run_delegations.slides_request_id=excluded.slides_request_id)`, key.PageID, id, run, key.WorkspaceID, id, record.Admission.ChatID, record.Admission.AgentID, record.Admission.HumanUserID)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return errSlideAccess
		}
		return nil
	})
}
