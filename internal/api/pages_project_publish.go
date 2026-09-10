package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

type pageProjectPublishRequest struct {
	BuildID             string `json:"build_id" yaml:"build_id"`
	ExpectedRevision    int64  `json:"expected_revision" yaml:"expected_revision"`
	ExpectedPublication *int64 `json:"expected_publication" yaml:"expected_publication"`
	ReviewedCode        bool   `json:"reviewed_code" yaml:"reviewed_code"`
	RollbackVersion     int64  `json:"rollback_version,omitempty" yaml:"rollback_version,omitempty"`
	// The fence: the two bases the human actually reviewed.
	//
	// expected_publication already fences the publication counter and the
	// draft CAS already fences the source revision, but neither says anything
	// about the DECLARATION the reviewer read. Another writer restoring a
	// panel version, or a routine definition changing under a `call` action,
	// moves what this publication will bind to without moving either counter.
	// Both are required: an optional fence is a fence the caller forgets, and
	// the point of the review snapshot is that a refetch at publish time
	// cannot supply this property.
	ExpectedDefinitionDigest string            `json:"expected_definition_digest" yaml:"expected_definition_digest"`
	ExpectedRoutineDigests   map[string]string `json:"expected_routine_digests" yaml:"expected_routine_digests"`
}

// replyPublishConflict writes a 409 that names WHICH base moved.
//
// "Something changed; reload" leaves the reviewer to guess whether to re-read
// the diff, the declaration or the routine scripts. The four kinds mirror
// PublishConflictKind in lib/pages/editor-contract.ts.
func replyPublishConflict(w http.ResponseWriter, msg, kind string, routines []string) {
	body := map[string]any{"error": msg, "conflict": kind}
	if len(routines) > 0 {
		body["routines"] = routines
	}
	writeJSON(w, 409, body)
}

// validHexDigest accepts exactly what sha256 hex output is: 64 lowercase hex
// bytes. An uppercase or truncated digest is a caller bug, not a conflict, so
// it is a 400 and never a silent pass.
func validHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// pageRoutineDigestsIn records the routine definitions observed during review.
// These hashes are provenance, not a promise to freeze routine scripts or
// execution.
//
// It takes an explicit querier so the publish fence can recompute the
// candidate's digests INSIDE its transaction rather than from a snapshot taken
// before the transaction opened — which is the difference between fencing the
// publication and describing it. The pre-publication check passes h.db and
// gets the same answer through the same code, so the two can never drift.
// A routine the candidate calls but this workspace no longer has comes back as
// `unresolved` rather than as an error: it is a reviewable fact about the
// candidate, not a storage failure, and the caller owes the human a sentence
// naming it. Only a real query failure comes back as err.
func pageRoutineDigestsIn(ctx context.Context, q pageRowQuerier, ws string, doc *pages.Document) (map[string]string, string, error) {
	result := map[string]string{}
	for _, panel := range doc.Spec.Panels {
		for _, action := range panel.Actions {
			if action.Kind != pages.ActionCall || result[action.Routine] != "" {
				continue
			}
			var definition string
			err := q.QueryRowContext(ctx, `SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, ws, action.Routine).Scan(&definition)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, action.Routine, nil
			}
			if err != nil {
				return nil, "", fmt.Errorf("read action routine %q: %w", action.Routine, err)
			}
			result[action.Routine] = pageRoutineDigest(definition)
		}
	}
	return result, "", nil
}

// pageUnresolvedRoutineMessage is the one sentence a caller gets when the
// candidate calls a routine that is gone. `sql: no rows in result set` used to
// reach the wire through err.Error(); a driver string is not an API contract,
// and it does not tell the reader which routine to go and fix.
func pageUnresolvedRoutineMessage(routine string) string {
	return fmt.Sprintf("This candidate has an action calling routine %q, which no longer exists in this workspace; restore the routine or remove the action before publishing.", routine)
}

type pageRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// movedRoutines names every routine whose digest the reviewer would not
// recognise: added, removed, or changed. Sorted so the message is stable.
func movedRoutines(expected, current map[string]string) []string {
	moved := []string{}
	for name, digest := range current {
		if expected[name] != digest {
			moved = append(moved, name)
		}
	}
	for name := range expected {
		if _, ok := current[name]; !ok {
			moved = append(moved, name)
		}
	}
	sort.Strings(moved)
	return moved
}

type pagePublication struct {
	Version        int64  `json:"version"`
	BuildID        string `json:"build_id"`
	SourceRevision int64  `json:"source_revision"`
	SourceDigest   string `json:"source_digest"`
	GitCommit      string `json:"git_commit"`
	ArtifactDigest string `json:"artifact_digest"`
	CreatedAt      string `json:"created_at"`
}
type checkedPageBuild struct {
	record   pagePublication
	spec     string
	document *pages.Document
	artifact *pagebuild.Artifact
	report   map[string]any
	resolved map[string]resolvedPanel
	gates    *gatePlan
}

func (h *PageHandler) checkPageCandidate(w http.ResponseWriter, r *http.Request, rec *pageRecord, build string, revision int64) (*checkedPageBuild, bool) {
	if h.pageArtifacts == nil || h.pageRuntimeOrigin == "" {
		replyError(w, 503, "Page build worker and separate runtime origin must be configured")
		return nil, false
	}
	var row pagePublication
	var state, spec string
	err := h.db.QueryRowContext(r.Context(), `SELECT b.id,b.source_revision,b.source_digest,COALESCE(b.artifact_digest,''),b.state,v.git_commit,v.spec_json FROM page_project_builds b JOIN page_project_revisions v ON v.page_id=b.page_id AND v.revision=b.source_revision WHERE b.page_id=? AND b.id=?`, rec.ID, build).Scan(&row.BuildID, &row.SourceRevision, &row.SourceDigest, &row.ArtifactDigest, &state, &row.GitCommit, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Page build not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "read publish candidate", err)
		return nil, false
	}
	if state != "ready" || row.SourceRevision != revision || row.GitCommit == "" || spec == "" {
		replyError(w, 409, "A ready build of the selected Git source revision is required")
		return nil, false
	}
	source, archived, err := h.projectStore.ReadCheckpoint(r.Context(), WorkspaceIDFromContext(r.Context()), rec.ID, row.GitCommit)
	if err != nil {
		replyInternalError(w, h.logger, "verify publish source", err)
		return nil, false
	}
	digest, err := source.Digest()
	if err != nil || digest != row.SourceDigest || archived != spec {
		replyError(w, 409, "Publish source integrity check failed")
		return nil, false
	}
	artifact, err := h.pageArtifacts.Get(r.Context(), WorkspaceIDFromContext(r.Context()), row.ArtifactDigest)
	if err != nil {
		replyInternalError(w, h.logger, "verify publish artifact", err)
		return nil, false
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		replyInternalError(w, h.logger, "read publish definition", err)
		return nil, false
	}
	if doc.Metadata.Slug != rec.Slug {
		replyError(w, 409, "Publish definition has a different Page identity")
		return nil, false
	}
	if err := validatePortableDraft(&doc); err != nil {
		replyError(w, 422, err.Error())
		return nil, false
	}
	resolved, ok := h.resolveReferences(w, r, WorkspaceIDFromContext(r.Context()), &doc)
	if !ok {
		return nil, false
	}
	gates, ok := h.resolveGates(w, r, WorkspaceIDFromContext(r.Context()), &doc)
	if !ok {
		return nil, false
	}
	routines, unresolved, err := pageRoutineDigestsIn(r.Context(), h.db, WorkspaceIDFromContext(r.Context()), &doc)
	if err != nil {
		replyInternalError(w, h.logger, "read candidate routine definitions", err)
		return nil, false
	}
	if unresolved != "" {
		replyError(w, 422, pageUnresolvedRoutineMessage(unresolved))
		return nil, false
	}
	report := map[string]any{"routine_definitions": routines, "routine_revision_pinned": false, "source_integrity": "pass", "artifact_integrity": "pass", "typecheck": "pass", "build": "pass", "bindings": "pass", "toolchain": artifact.Toolchain, "browser_review": "required", "security_review": "required"}
	return &checkedPageBuild{record: row, spec: spec, document: &doc, artifact: artifact, report: report, resolved: resolved, gates: gates}, true
}
func (h *PageHandler) CheckProject(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	raw, ok := readCapped(w, r, 1024, "Page check")
	if !ok {
		return
	}
	var req struct {
		BuildID          string `json:"build_id" yaml:"build_id"`
		ExpectedRevision int64  `json:"expected_revision" yaml:"expected_revision"`
	}
	if err := pages.DecodeProjectJSON(raw, &req); err != nil || req.BuildID == "" || req.ExpectedRevision < 1 {
		replyError(w, 400, "build_id and expected_revision are required")
		return
	}
	candidate, ok := h.checkPageCandidate(w, r, rec, req.BuildID, req.ExpectedRevision)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"build_id": req.BuildID, "source_revision": candidate.record.SourceRevision, "git_commit": candidate.record.GitCommit, "artifact_digest": candidate.record.ArtifactDigest, "checks": candidate.report})
}
func (h *PageHandler) PublishProject(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	user := UserFromContext(r.Context())
	ws := WorkspaceIDFromContext(r.Context())
	if !h.mayAdministerGrants(r.Context(), ws, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, 403, "Publishing requires Page ownership or workspace administration")
		return
	}
	// 32 KiB, not the previous 2 KiB: expected_routine_digests carries one
	// 64-hex digest per distinct `call` routine, and a Page may declare
	// MaxPanelsPerPage (24) x MaxActionsPerPanel (6) = 144 of them with
	// 64-byte slugs — about 19 KiB of map alone, before the fixed fields.
	raw, ok := readCapped(w, r, 32<<10, "Page publication")
	if !ok {
		return
	}
	var req pageProjectPublishRequest
	if err := pages.DecodeProjectJSON(raw, &req); err != nil || req.ExpectedPublication == nil || *req.ExpectedPublication < 0 || !req.ReviewedCode || req.RollbackVersion < 0 {
		replyError(w, 400, "expected_publication and reviewed_code=true are required")
		return
	}
	// Shape only. The comparison itself happens inside the transaction below.
	if !validHexDigest(req.ExpectedDefinitionDigest) {
		replyError(w, 400, "expected_definition_digest must be the 64-character lowercase sha256 of the reviewed Page definition")
		return
	}
	if req.ExpectedRoutineDigests == nil {
		replyError(w, 400, "expected_routine_digests is required; send {} when the reviewed candidate declares no call actions")
		return
	}
	for routine, digest := range req.ExpectedRoutineDigests {
		if !validHexDigest(digest) {
			replyError(w, 400, "expected_routine_digests["+routine+"] must be a 64-character lowercase sha256 digest")
			return
		}
	}
	if req.RollbackVersion > 0 {
		if req.BuildID != "" || req.ExpectedRevision != 0 {
			replyError(w, 400, "rollback_version cannot be combined with a build or draft revision")
			return
		}
		if err := h.db.QueryRowContext(r.Context(), `SELECT build_id,source_revision FROM page_project_publications WHERE page_id=? AND version=?`, rec.ID, req.RollbackVersion).Scan(&req.BuildID, &req.ExpectedRevision); errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "publication not found")
			return
		} else if err != nil {
			replyInternalError(w, h.logger, "read rollback publication", err)
			return
		}
	}
	if req.BuildID == "" || req.ExpectedRevision < 1 {
		replyError(w, 400, "build_id and expected_revision are required")
		return
	}
	// An exact retry returns its receipt without changing the current pointer.
	var previous pagePublication
	err := h.db.QueryRowContext(r.Context(), `SELECT version,build_id,source_revision,source_digest,git_commit,artifact_digest,created_at FROM page_project_publications WHERE page_id=? AND version=? AND build_id=? AND source_revision=? AND actor_user_id=? AND COALESCE(rollback_of,0)=?`, rec.ID, *req.ExpectedPublication+1, req.BuildID, req.ExpectedRevision, user.ID, req.RollbackVersion).Scan(&previous.Version, &previous.BuildID, &previous.SourceRevision, &previous.SourceDigest, &previous.GitCommit, &previous.ArtifactDigest, &previous.CreatedAt)
	if err == nil {
		h.writePublicationReceipt(w, r, rec.ID, previous, true)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read publication receipt", err)
		return
	}
	var retainedPublications int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_project_publications WHERE page_id=?`, rec.ID).Scan(&retainedPublications); err != nil {
		replyInternalError(w, h.logger, "count retained publications", err)
		return
	}
	if retainedPublications >= 512 {
		replyError(w, 507, "Page publication quota reached")
		return
	}
	candidate, ok := h.checkPageCandidate(w, r, rec, req.BuildID, req.ExpectedRevision)
	if !ok {
		return
	}
	shapes, err := h.livePanelShapes(r.Context(), rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read live panel shapes", err)
		return
	}
	dim := panelsToDim(candidate.document, candidate.resolved, shapes)
	now := h.evaluator().Now().UTC().Format(time.RFC3339Nano)
	report, _ := json.Marshal(candidate.report)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin Page publish", err)
		return
	}
	defer tx.Rollback()
	// The fence, before any write and inside the transaction that will do the
	// writing. Reading `before` here rather than earlier in the request is the
	// point: the pre-existing `WHERE spec_json=?` CAS only fenced the window
	// between this read and its own write, never the window since review.
	//
	// It runs AFTER the idempotent-retry short-circuit above, deliberately. An
	// exact retry is re-delivery of a request that already committed; failing
	// it because the world moved on afterwards would turn a delivered success
	// into a phantom 409 and invite a second publication of the same code. The
	// receipt records what happened, and the fence guards what is about to.
	var before string
	if err := tx.QueryRowContext(r.Context(), `SELECT spec_json FROM pages WHERE id=?`, rec.ID).Scan(&before); err != nil {
		replyInternalError(w, h.logger, "read current publication definition", err)
		return
	}
	if digest := pageDefinitionDigest(before); digest != req.ExpectedDefinitionDigest {
		replyPublishConflict(w, "The live Page definition changed since this candidate was reviewed; review the current definition before publishing", "definition", nil)
		return
	}
	// The draft CAS runs BEFORE the routine fence, not after.
	//
	// When a caller publishes a revision the draft has since moved past, both
	// checks fail — but for one reason, and only one of them names it. The
	// routine map was built for whichever document the caller believed was
	// current, so comparing it against THIS candidate's routines answers
	// `conflict:"routines"` naming routines nobody touched. Checking the draft
	// first lets the accurate `conflict:"draft"` win, and the caller rebuilds
	// against the revision that actually exists.
	if req.RollbackVersion == 0 {
		var revision int64
		var commit string
		if err := tx.QueryRowContext(r.Context(), `SELECT revision,git_commit FROM page_project_drafts WHERE page_id=?`, rec.ID).Scan(&revision, &commit); err != nil {
			replyInternalError(w, h.logger, "verify current draft", err)
			return
		}
		if revision != req.ExpectedRevision || commit != candidate.record.GitCommit {
			replyPublishConflict(w, "Draft changed; build and review the current revision", "draft", nil)
			return
		}
	}
	// Rollback publishes a retained artifact over the SAME live definition, so
	// it is fenced identically: restoring old code against a declaration the
	// reviewer never saw binds it to panels, producers and routines nobody
	// approved for it.
	currentRoutines, unresolvedRoutine, err := pageRoutineDigestsIn(r.Context(), tx, ws, candidate.document)
	if err != nil {
		replyInternalError(w, h.logger, "recompute candidate routine definitions", err)
		return
	}
	if unresolvedRoutine != "" {
		replyError(w, 422, pageUnresolvedRoutineMessage(unresolvedRoutine))
		return
	}
	if moved := movedRoutines(req.ExpectedRoutineDigests, currentRoutines); len(moved) > 0 {
		replyPublishConflict(w, "A routine this candidate calls changed since it was reviewed; review the current routine definitions before publishing", "routines", moved)
		return
	}
	version := *req.ExpectedPublication + 1
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,actor_user_id,created_at,rollback_of) VALUES(?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,0))`, rec.ID, version, candidate.record.BuildID, candidate.record.SourceRevision, candidate.record.SourceDigest, candidate.record.GitCommit, candidate.record.ArtifactDigest, candidate.spec, string(report), user.ID, now, req.RollbackVersion); err != nil {
		if isUniqueViolation(err) {
			replyPublishConflict(w, "Publication changed; reload before publishing", "publication", nil)
		} else {
			replyInternalError(w, h.logger, "record Page publication", err)
		}
		return
	}
	res, err := tx.ExecContext(r.Context(), `INSERT INTO page_project_live(page_id,version) VALUES(?,?) ON CONFLICT(page_id) DO UPDATE SET version=excluded.version,published=1 WHERE page_project_live.version=?`, rec.ID, version, *req.ExpectedPublication)
	if err != nil {
		replyInternalError(w, h.logger, "switch Page publication", err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		replyPublishConflict(w, "Publication changed; reload before publishing", "publication", nil)
		return
	}
	if *req.ExpectedPublication > 0 {
		var prior int
		if err := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM page_project_publications WHERE page_id=? AND version=?`, rec.ID, *req.ExpectedPublication).Scan(&prior); err != nil || prior != 1 {
			replyPublishConflict(w, "Expected publication does not exist", "publication", nil)
			return
		}
	}
	res, err = tx.ExecContext(r.Context(), `UPDATE pages SET name=?,description=NULLIF(?,''),spec_json=?,updated_at=? WHERE id=? AND spec_json=?`, candidate.document.Metadata.Name, candidate.document.Metadata.Description, candidate.spec, now, rec.ID, before)
	if err != nil {
		replyInternalError(w, h.logger, "publish Page definition", err)
		return
	}
	n, err = res.RowsAffected()
	if err != nil || n != 1 {
		replyPublishConflict(w, "Page definition changed; review again", "definition", nil)
		return
	}
	if err := reconcilePanels(r.Context(), tx, rec.ID, candidate.document, candidate.resolved, now); err != nil {
		replyInternalError(w, h.logger, "publish Page panels", err)
		return
	}
	if err := clearPanelRings(r.Context(), tx, rec.ID, dim); err != nil {
		replyInternalError(w, h.logger, "clear redefined Page data", err)
		return
	}
	if err := reconcileWakeAutomations(r.Context(), tx, ws, rec.ID, rec.Slug, candidate.gates, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "publish Page automations", err)
		return
	}
	var seq int64
	if err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(seq),0)+1 FROM page_versions WHERE page_id=?`, rec.ID).Scan(&seq); err != nil {
		replyInternalError(w, h.logger, "allocate Page version", err)
		return
	}
	if err := insertPageVersion(r.Context(), tx, rec.ID, seq, candidate.spec, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "record published definition", err)
		return
	}
	if err := trimPageVersions(r.Context(), tx, rec.ID, seq); err != nil {
		replyInternalError(w, h.logger, "trim Page versions", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit Page publication", err)
		return
	}
	h.refreshAutomations(r.Context())
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"slug": rec.Slug, "page_id": rec.ID})
	candidate.record.Version = version
	candidate.record.CreatedAt = now
	w.Header().Set("Cache-Control", "no-store")
	h.writePublicationReceipt(w, r, rec.ID, candidate.record, false)
}

// A receipt records a completed operation, not a promise that its version is still live.
func (h *PageHandler) writePublicationReceipt(w http.ResponseWriter, r *http.Request, page string, receipt pagePublication, replayed bool) {
	var live int64
	var published bool
	if err := h.db.QueryRowContext(r.Context(), `SELECT version,published FROM page_project_live WHERE page_id=?`, page).Scan(&live, &published); err != nil {
		replyInternalError(w, h.logger, "read publication receipt state", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		pagePublication
		LiveVersion int64 `json:"live_version"`
		Published   bool  `json:"published"`
		IsCurrent   bool  `json:"is_current"`
		Replayed    bool  `json:"replayed"`
	}{receipt, live, published, published && live == receipt.Version, replayed})
}

// Runtime delivery uses the Page's normal reach gate, not source-edit authority.
func (h *PageHandler) PageApplication(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Unauthorized")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	rec, err := h.loadPage(r.Context(), ws, r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Page not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read application Page", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), ws, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read application panels", err)
		return
	}
	viewer, err := h.loadViewer(r.Context(), ws, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read application viewer", err)
		return
	}
	reachable, err := h.canSeePage(r.Context(), ws, rec, panels, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "check application reach", err)
		return
	}
	if !reachable {
		replyError(w, 404, "Page not found")
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	var publication pagePublication
	var published bool
	err = h.db.QueryRowContext(r.Context(), `SELECT p.version,p.build_id,p.source_revision,p.source_digest,p.git_commit,p.artifact_digest,p.created_at,l.published FROM page_project_live l JOIN page_project_publications p ON p.page_id=l.page_id AND p.version=l.version WHERE l.page_id=?`, rec.ID).Scan(&publication.Version, &publication.BuildID, &publication.SourceRevision, &publication.SourceDigest, &publication.GitCommit, &publication.ArtifactDigest, &publication.CreatedAt, &published)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 200, map[string]any{"publication": nil, "publication_version": 0, "can_publish": h.mayAdministerGrants(r.Context(), ws, user.ID, RoleFromContext(r.Context()), rec)})
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read application publication", err)
		return
	}
	if !published {
		writeJSON(w, 200, map[string]any{"publication": nil, "publication_version": publication.Version, "can_publish": h.mayAdministerGrants(r.Context(), ws, user.ID, RoleFromContext(r.Context()), rec)})
		return
	}
	if h.pageArtifacts == nil || h.pageRuntimeOrigin == "" {
		replyError(w, 503, "Page runtime is not configured")
		return
	}
	scheme := "http://"
	if strings.HasPrefix(h.pageRuntimeOrigin, "https://") {
		scheme = "https://"
	}
	if err := pagebuild.ValidateRuntimeOriginForDevelopment(h.pageRuntimeOrigin, scheme+r.Host, h.pageRuntimeDevelopmentSameOrigin); err != nil {
		replyError(w, 503, "Page runtime does not isolate this Studio host")
		return
	}
	// Reauthorize before conditional delivery. The browser retains the immutable
	// artifact in its active query, not a public HTTP cache; revocation still
	// fails the reach gate above instead of serving an unconditional 304.
	canPublish := h.mayAdministerGrants(r.Context(), ws, user.ID, RoleFromContext(r.Context()), rec)
	identity, _ := json.Marshal([]any{ws, rec.ID, publication, h.pageRuntimeOrigin, canPublish, h.pageRuntimeDevelopmentSameOrigin})
	etag := fmt.Sprintf("\"%x\"", sha256.Sum256(identity))
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	artifact, err := h.pageArtifacts.Get(r.Context(), ws, publication.ArtifactDigest)
	if err != nil {
		replyInternalError(w, h.logger, "read published application", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"publication": publication, "publication_version": publication.Version, "can_publish": canPublish, "artifact": artifact, "development_same_origin": h.pageRuntimeDevelopmentSameOrigin, "runtime_url": strings.TrimSuffix(h.pageRuntimeOrigin, "/") + pagebuild.RuntimePath})
}
