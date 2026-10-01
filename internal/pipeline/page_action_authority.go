package pipeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/crewship-ai/crewship/internal/pages"
)

const pageActionAuthorityPrefix = "page.action.v1:"

// PageActionInvocation identifies the ORIGINAL admission, including inside a
// nested routine. These values are stamped by the server, never run metadata.
// The encoding uses the existing durable authority column so it follows the
// actor atomically through coalescing, dispatch, nested calls and recovery.
type PageActionInvocation struct {
	PageID       string `json:"page_id"`
	PanelID      string `json:"panel_id"`
	ActionID     string `json:"action_id"`
	PipelineID   string `json:"pipeline_id"`
	ActionDigest string `json:"action_digest"`
	Publication  int64  `json:"publication,omitempty"`
}

func (a PageActionInvocation) Authority() string {
	data, _ := json.Marshal(a) // strings and int64 cannot fail JSON encoding
	return pageActionAuthorityPrefix + string(data)
}

func checkPageActionAuthority(ctx context.Context, db PageActionQuery, in RunInput, role string) error {
	var a PageActionInvocation
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(in.InvocationAuthority, pageActionAuthorityPrefix)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a); err != nil || a.PageID == "" || a.PanelID == "" || a.ActionID == "" || a.PipelineID == "" || a.ActionDigest == "" || a.Publication < 0 {
		return ErrInvocationAuthorityRevoked
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ErrInvocationAuthorityRevoked
	}
	var spec, slug, status string
	var crewMember bool
	err := db.QueryRowContext(ctx, `SELECT p.spec_json, r.slug, r.status,
 EXISTS(SELECT 1 FROM crew_members cm WHERE cm.crew_id=c.id AND cm.user_id=?)
 FROM pages p JOIN page_panels pp ON pp.page_id=p.id AND pp.panel_id=?
 JOIN crews c ON c.id=pp.owner_crew_id AND c.workspace_id=p.workspace_id AND c.deleted_at IS NULL
 JOIN pipelines r ON r.id=? AND r.workspace_id=p.workspace_id AND r.deleted_at IS NULL
 WHERE p.id=? AND p.workspace_id=?`, in.InvokingUserID, a.PanelID, a.PipelineID, a.PageID, in.WorkspaceID).Scan(&spec, &slug, &status, &crewMember)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvocationAuthorityRevoked
	}
	if err != nil {
		return fmt.Errorf("page action authority lookup: %w", err)
	}
	if !pages.CanSeePanel(role, crewMember) || !routineStatusRunnable(status) {
		return ErrInvocationAuthorityRevoked
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		return fmt.Errorf("page action authority spec: %w", err)
	}
	panel, ok := doc.FindPanel(a.PanelID)
	if !ok {
		return ErrInvocationAuthorityRevoked
	}
	action, ok := panel.FindAction(a.ActionID)
	if !ok || action.Kind != pages.ActionCall || action.Routine != slug || PageActionDigest(action) != a.ActionDigest {
		return ErrInvocationAuthorityRevoked
	}
	if a.Publication > 0 {
		var current bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_project_live l
   JOIN page_project_publications pub ON pub.page_id=l.page_id AND pub.version=l.version
   WHERE l.page_id=? AND l.version=? AND l.published=1 AND pub.spec_json=?)`, a.PageID, a.Publication, spec).Scan(&current)
		if err != nil {
			return fmt.Errorf("page action publication authority: %w", err)
		}
		if !current {
			return ErrInvocationAuthorityRevoked
		}
	}
	return nil
}

// PageActionDigest binds the admitted action contract, including fixed params
// and input declarations. Editing that contract revokes already queued work.
func PageActionDigest(action *pages.PanelAction) string {
	data, err := json.Marshal(action)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
