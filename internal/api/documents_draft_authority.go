package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/documents"
)

type documentSemanticEdit struct {
	From   int    `json:"from"`
	To     int    `json:"to"`
	Insert string `json:"insert"`
}
type documentSemanticPatch struct {
	Target string                `json:"target"`
	Text   string                `json:"text"`
	Edit   *documentSemanticEdit `json:"edit"`
	Rich   *documentRichProof    `json:"rich,omitempty"`
}
type documentDraftScope struct {
	PageID     string
	Generation int64
	Rev        int64
	// Schema is the stored content schema the run drafts or proposes against;
	// documents.SchemaAllows says which.
	Schema int
	// Propose is a propose mandate (#5137, #5233): proposals only, never commits.
	Propose bool
	// Blocks, comma-separated, are the only blocks a proposal may target; "" is the whole document.
	Blocks string
	// Read is a flag-on run's read through its scope (#5317 5.3): no
	// consent, never a lease, commit, checkpoint or proposal.
	Read bool
	// Reach is why a flag-on run reaches the Document, for its audit:
	// consented (the drafting or iteration consent with a page grant or the
	// human's own Document) or folder (a folder of its crew at propose).
	Reach string
}

// documentStatePurposeHeader names the Node operation behind a state
// callback. Only the private, service-authenticated Docs authority reads it.
const documentStatePurposeHeader = "X-Crewship-Docs-Purpose"

// documentViewReadKey marks a state callback that Node makes for its "read"
// operation, the one behind the agent draft view (#5317 5.3).
type documentViewReadKey struct{}

// documentStateLoadedForView: Node names exactly one operation, "read".
func documentStateLoadedForView(r *http.Request) bool {
	values := r.Header.Values(documentStatePurposeHeader)
	return len(values) == 1 && values[0] == "read"
}

// admits reports whether a block is inside this mandate.
func (s documentDraftScope) admits(block string) bool {
	return (documents.IterationIntent{Schema: documents.FormattedSchema, Blocks: s.Blocks}).Admits(block)
}

// The private Node callback has the original bearer, not crew IPC. Resolve only
// its candidate transport, then authenticate the HMAC with the existing verifier.
// Current authority is checked again inside every storage transaction.
func (h *documentAuthority) authenticateDocumentDraftAgent(w http.ResponseWriter, r *http.Request, operation http.Handler) bool {
	value := extractToken(r)
	if !strings.HasPrefix(value, "agtv2.") {
		return false
	}
	fields := strings.Split(value, ".")
	if len(value) > 2048 || len(fields) != 5 {
		replyError(w, 403, "invalid run capability")
		return true
	}
	run, err := base64.RawURLEncoding.DecodeString(fields[3])
	if err != nil {
		replyError(w, 403, "invalid run capability")
		return true
	}
	var workspace, crew string
	if h.pages.db.QueryRowContext(r.Context(), `SELECT workspace_id,crew_id FROM artifact_run_principals WHERE run_id=?`, string(run)).Scan(&workspace, &crew) != nil || workspace != r.Header.Get("X-Workspace-ID") {
		replyError(w, 403, "invalid run capability")
		return true
	}
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, crew)
	if checked, ok := h.pages.artifactAgentRequest(w, r.WithContext(ctx)); ok {
		operation.ServeHTTP(w, checked)
	}
	return true
}

// Creation provenance is not a grant. Only the original explicit, unexpired
// drafting purpose can reach its one created Page, still owned by that human.
func (h *PageHandler) documentDraftScope(ctx context.Context, a *artifactRunAuthority, page string) (documentDraftScope, error) {
	var scope documentDraftScope
	selected, err := h.documentIterationRun(ctx, a)
	if err != nil {
		return scope, err
	}
	if selected {
		return h.documentIterationScope(ctx, a, page)
	}
	consent, err := h.documentCreationAuthority(ctx, a)
	if err != nil || !consent.Consumed.Valid || !consent.Page.Valid || page != "" && page != consent.Page.String {
		return scope, errDocumentAccess
	}
	var generation, head int64
	var slug string
	err = h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT p.id,p.slug,c.draft_generation,c.draft_rev,d.generation,d.head_rev,d.schema_version
 FROM document_creation_consents c
 JOIN document_creations origin ON origin.page_id=c.result_page_id AND origin.workspace_id=c.workspace_id AND origin.run_id=c.run_id AND origin.human_user_id=c.human_user_id AND origin.source_chat_id=c.chat_id AND origin.submission_id=c.submission_id
 JOIN pages p ON p.id=c.result_page_id AND p.workspace_id=c.workspace_id AND p.owner_user_id=c.human_user_id AND p.owner_crew_id IS NULL
 JOIN documents d ON d.page_id=p.id AND d.workspace_id=p.workspace_id
 WHERE c.run_id=? AND c.workspace_id=? AND c.human_user_id=? AND c.chat_id=? AND c.message_id=? AND c.drafting_allowed=1 AND origin.agent_id=?`, a.RunID, a.WorkspaceID, a.HumanUserID, a.ChatID, a.MessageID, a.AgentID).Scan(&scope.PageID, &slug, &scope.Generation, &scope.Rev, &generation, &head, &scope.Schema)
	if err != nil {
		return scope, errDocumentAccess
	}
	if !documents.SchemaAllows(scope.Schema, documents.CapDraft) {
		return scope, errDocumentAccess
	}
	if generation != scope.Generation || head != scope.Rev {
		return scope, documents.ErrConflict
	}
	role, err := h.liveContentHumanRole(ctx, a.WorkspaceID, a.HumanUserID)
	if err != nil {
		return scope, errDocumentAccess
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	rec, err := h.loadPage(ctx, a.WorkspaceID, slug)
	if err != nil || rec.HasProject || rec.HasApplication || !h.mayEditSpec(ctx, a.WorkspaceID, a.HumanUserID, role, rec) {
		return scope, errDocumentAccess
	}
	var panels int
	if h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM page_panels WHERE page_id=?`, scope.PageID).Scan(&panels) != nil || panels != 0 {
		return scope, errDocumentAccess
	}
	if _, _, err = privatePageOwnershipSQL(ctx, h.pageAuthReader(ctx), "p"); err != nil {
		return scope, errDocumentAccess
	}
	return scope, nil
}

func (h *PageHandler) authorizeDocumentDraft(ctx context.Context, a *artifactRunAuthority, k documents.Key, operation string) (string, error) {
	switch operation {
	case "state", "receipt", "lease", "renew", "release", "commit", "checkpoint", "propose":
	default:
		return "", errDocumentAccess
	}
	if k.WorkspaceID != a.WorkspaceID {
		return "", errDocumentAccess
	}
	scope, err := h.documentDraftScope(ctx, a, k.PageID)
	if err != nil {
		// Flag on (#5317 5.3): without a consent for this Document, the run
		// may still read it through its scope (recorded there), but only for
		// Node's draft-view read: never for an export, comments, checkpoints,
		// a lease, commit, checkpoint or proposal.
		if viewRead, _ := ctx.Value(documentViewReadKey{}).(bool); operation != "state" || !viewRead || !errors.Is(err, errDocumentAccess) {
			return "", err
		}
		if _, scoped := h.documentScopedRunRead(ctx, a, k.PageID); scoped != nil {
			if errors.Is(scoped, errArtifactRunCap) {
				return "", scoped
			}
			return "", err
		}
		return "agent:" + a.AgentID + ":" + a.RunID, nil
	}
	// Flag on (#5317 5.3): the drafting or iteration consent stays the only
	// reach (it binds the generation, revision and lease of every write); the
	// content access is audited with its human in this transaction, failing
	// closed. Leases carry no content and are not recorded. A proposal is
	// audited as propose, never as write.
	//
	// Folder reach (#5317 5.2) proposes and reads only: a commit or checkpoint
	// is refused here as well as by the propose mandate's guard, and each
	// access is charged to the run's read limit like any folder read.
	folder := scope.Reach == runReachFolder
	// A propose mandate never needs the writer lease (a proposal is not a
	// commit). With the flag on the run may not take or renew it, so it can
	// never hold the lease against a human Editor; release stays allowed.
	if (operation == "lease" || operation == "renew") && scope.Propose && h.artifactRunModesOn(ctx, a.WorkspaceID) {
		return "", errDocumentAccess
	}
	audited := ""
	switch operation {
	case "state", "receipt":
		audited = "read"
	case "propose":
		audited = "propose"
	case "commit", "checkpoint":
		if folder {
			return "", errDocumentAccess
		}
		audited = "write"
	}
	if audited != "" {
		tx, _ := ctx.Value(pageAuthReaderKey{}).(*sql.Tx)
		reach := runReachConsented
		if folder {
			reach = runReachFolder
			if tx == nil {
				return "", errDocumentAccess
			}
			if err := chargeArtifactRunRead(context.WithValue(ctx, pageAuthReaderKey{}, tx), tx, a, a.WorkspaceID, k.PageID); err != nil {
				if errors.Is(err, errArtifactRunCap) {
					return "", err
				}
				return "", errDocumentAccess
			}
		}
		if h.recordKindRunAccess(ctx, tx, a, k.PageID, reach, audited) != nil {
			return "", errDocumentAccess
		}
	}
	return "agent:" + a.AgentID + ":" + a.RunID, nil
}

// This guard runs AFTER exact receipt replay and inside Commit's transaction.
// Advancing the server cursor rolls back with any subsequent failed write.
func (h *PageHandler) documentDraftGuard(a *artifactRunAuthority, k documents.Key, operation string, ifRev int64) documents.MutationGuard {
	return func(ctx context.Context, tx *sql.Tx, actor string, state documents.State) error {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		selected, err := h.documentIterationRun(ctx, a)
		if err != nil {
			return err
		}
		if selected {
			return h.documentIterationGuard(a, k, operation, ifRev)(ctx, tx, actor, state)
		}
		scope, err := h.documentDraftScope(ctx, a, k.PageID)
		if err != nil {
			return err
		}
		if actor != "agent:"+a.AgentID+":"+a.RunID || ifRev != scope.Rev || state.HeadRev != scope.Rev || state.Generation != scope.Generation {
			return documents.ErrConflict
		}
		if operation == "checkpoint" {
			return nil
		}
		if operation != "commit" {
			return errDocumentAccess
		}
		result, err := tx.ExecContext(ctx, `UPDATE document_creation_consents SET draft_rev=? WHERE run_id=? AND result_page_id=? AND drafting_allowed=1 AND draft_generation=? AND draft_rev=?`, scope.Rev+1, a.RunID, k.PageID, scope.Generation, scope.Rev)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return documents.ErrConflict
		}
		return nil
	}
}

func documentDraftRun(ctx context.Context) (*artifactRunAuthority, error) {
	a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	if !ok || a == nil {
		return nil, errors.New("live draft run required")
	}
	return a, nil
}
