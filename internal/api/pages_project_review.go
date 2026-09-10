package api

// The authorized review snapshot (docs/prd/pages.md §11, editor contract
// `lib/pages/editor-contract.ts` → `ReviewSnapshotWire`).
//
// Publishing an application replaces executable code AND the Page declaration
// that binds it to crews, producers and routines. The reviewer therefore has
// to be shown three different bases at once — the candidate source revision,
// the live publication, and the live panel definition — and they are three
// separate series that never line up.
//
// The endpoint exists so the values the human reviewed can be handed back to
// the publish call as a fence (`expected_definition_digest`,
// `expected_routine_digests`). Refetching in the browser cannot provide that
// property: it re-reads the world at publish time, which is exactly the window
// the fence closes.
//
// Two truths this response refuses to blur:
//
//   - A missing retained baseline is NOT "no changes". `source_available:false`
//     with a concrete reason and a `baseline_unavailable` blocker, never an
//     empty diff that reads as agreement.
//   - A routine with no recorded digest is `unknown`, never `unchanged`. The
//     publication's `checks_json` is provenance; a gap in it is a gap, not a
//     guarantee.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

type reviewActorWire struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Resolved NOW against the current directory, never a name snapshot taken
	// at the time of the change: `actor_json` stores ids only. Omitted rather
	// than invented when the id no longer resolves.
	Label string `json:"label,omitempty"`
}

type reviewBuildWire struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	ArtifactDigest string `json:"artifact_digest"`
	Error          string `json:"error,omitempty"`
}

type reviewCandidateWire struct {
	Revision     int64            `json:"revision"`
	GitCommit    string           `json:"git_commit"`
	SourceDigest string           `json:"source_digest"`
	CreatedAt    string           `json:"created_at"`
	Actor        reviewActorWire  `json:"actor"`
	Build        *reviewBuildWire `json:"build"`
}

type reviewBaselineWire struct {
	PublicationVersion int64   `json:"publication_version"`
	Published          bool    `json:"published"`
	DefinitionDigest   string  `json:"definition_digest"`
	SourceRevision     *int64  `json:"source_revision"`
	GitCommit          *string `json:"git_commit"`
	SourceAvailable    bool    `json:"source_available"`
	// A sentence, not a code: it is shown where the diff would have been.
	SourceUnavailableReason *string `json:"source_unavailable_reason"`
}

type reviewRoutineWire struct {
	Routine         string  `json:"routine"`
	PublishedDigest *string `json:"published_digest"`
	CurrentDigest   *string `json:"current_digest"`
	State           string  `json:"state"`
}

type reviewBlockerWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type reviewCapabilitiesWire struct {
	MayEditSpec bool `json:"may_edit_spec"`
	MayPublish  bool `json:"may_publish"`
}

type reviewSnapshotWire struct {
	IssuedAt           string                 `json:"issued_at"`
	Candidate          *reviewCandidateWire   `json:"candidate"`
	Baseline           reviewBaselineWire     `json:"baseline"`
	Routines           []reviewRoutineWire    `json:"routines"`
	Capabilities       reviewCapabilitiesWire `json:"capabilities"`
	Blockers           []reviewBlockerWire    `json:"blockers"`
	InitialPublication bool                   `json:"initial_publication"`
}

// Blocker codes, spelled once. The UI shows the message; tests assert the code.
const (
	reviewBlockerNoCandidate        = "no_candidate"
	reviewBlockerMatchesLive        = "candidate_matches_live"
	reviewBlockerBuildMissing       = "build_missing"
	reviewBlockerBuildFailed        = "build_failed"
	reviewBlockerBuildStale         = "build_stale"
	reviewBlockerBaselineMissing    = "baseline_unavailable"
	reviewBlockerDefinitionMoved    = "definition_moved"
	reviewBlockerNotPermitted       = "not_permitted"
	reviewBlockerStorageUnavailable = "storage_unavailable"
)

// pageDefinitionDigest is the value the publish fence compares: sha256 over the
// exact stored `pages.spec_json` bytes. Hashing the bytes rather than a
// re-marshalled document means a formatting-only rewrite still counts as a
// move, which is the conservative direction for a fence.
func pageDefinitionDigest(spec string) string {
	sum := sha256.Sum256([]byte(spec))
	return hex.EncodeToString(sum[:])
}

// ReviewProject serves the snapshot a human reviews and then publishes against.
//
// It is deliberately a 200 even when publishing is impossible: a reviewer who
// cannot publish still needs to read the candidate, and a Page whose artifact
// store is unconfigured still has a definition worth showing. Every refusal is
// a blocker in the body, never a status code that erases the rest.
//
// Two candidates, one shape. Without `?publication=N` the candidate is the
// current draft. With it, the candidate is retained publication N — what
// `rollback_version: N` would publish. That mode exists because the fence
// recomputes `expected_routine_digests` from the ARCHIVED spec of the version
// being restored, and nothing else on the wire tells a caller that key set:
// the draft's routines are a different document's routines, and deriving the
// set from the archived spec client-side would still leave the caller without
// each routine's current digest. Only the server can answer both halves, so
// it answers them here rather than making the browser guess and get a 400.
//
// `baseline` is the same in both modes: publishing a retained version replaces
// the same live definition, so it is fenced against the same digest.
func (h *PageHandler) ReviewProject(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	ctx := r.Context()
	ws := WorkspaceIDFromContext(ctx)
	user := UserFromContext(ctx)
	// A malformed cursor is a caller error, not a blocker: there is no
	// candidate the caller could have meant.
	requested := int64(0)
	if raw := r.URL.Query().Get("publication"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			replyError(w, 400, "publication must be a positive publication version")
			return
		}
		requested = parsed
	}

	snapshot := reviewSnapshotWire{
		IssuedAt: h.evaluator().Now().UTC().Format(time.RFC3339),
		Routines: []reviewRoutineWire{},
		Blockers: []reviewBlockerWire{},
		Capabilities: reviewCapabilitiesWire{
			MayEditSpec: true, // projectPage already proved it.
			MayPublish:  user != nil && h.mayAdministerGrants(ctx, ws, user.ID, RoleFromContext(ctx), rec),
		},
	}
	blocked := func(code, message string) {
		snapshot.Blockers = append(snapshot.Blockers, reviewBlockerWire{Code: code, Message: message})
	}

	// The live Page declaration. This is the fence value, and it is read even
	// when nothing has ever been published: a Page always has a definition.
	var liveSpec string
	if err := h.db.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id=?`, rec.ID).Scan(&liveSpec); err != nil {
		replyInternalError(w, h.logger, "read live Page definition", err)
		return
	}
	snapshot.Baseline.DefinitionDigest = pageDefinitionDigest(liveSpec)

	var publications int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_project_publications WHERE page_id=?`, rec.ID).Scan(&publications); err != nil {
		replyInternalError(w, h.logger, "count Page publications", err)
		return
	}
	snapshot.InitialPublication = publications == 0

	var publishedSpec, publishedChecks, publishedSource string
	if !snapshot.InitialPublication {
		var revision int64
		var commit string
		err := h.db.QueryRowContext(ctx, `SELECT p.version,l.published,p.source_revision,p.git_commit,p.source_digest,p.spec_json,p.checks_json FROM page_project_live l JOIN page_project_publications p ON p.page_id=l.page_id AND p.version=l.version WHERE l.page_id=?`, rec.ID).
			Scan(&snapshot.Baseline.PublicationVersion, &snapshot.Baseline.Published, &revision, &commit, &publishedSource, &publishedSpec, &publishedChecks)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "read live publication", err)
			return
		}
		if err == nil {
			snapshot.Baseline.SourceRevision = &revision
			snapshot.Baseline.GitCommit = &commit
			snapshot.Baseline.SourceAvailable, snapshot.Baseline.SourceUnavailableReason = h.reviewBaselineSource(ctx, ws, rec.ID, commit, publishedSpec)
			if !snapshot.Baseline.SourceAvailable {
				blocked(reviewBlockerBaselineMissing, *snapshot.Baseline.SourceUnavailableReason)
			}
			// The live declaration drifting away from the one this publication
			// shipped is a real, detectable condition (a panel rollback, an
			// import): the reviewer is comparing against something the running
			// application never declared.
			if publishedSpec != liveSpec {
				blocked(reviewBlockerDefinitionMoved, "The live Page definition no longer matches the one published with the running application; review the current definition, not the publication's.")
			}
		} else {
			// Publications exist but no live pointer: nothing is running, so
			// there is no retained baseline to compare against either.
			reason := "This Page has publication history but no live publication pointer, so there is no retained application source to compare against."
			snapshot.Baseline.SourceUnavailableReason = &reason
			blocked(reviewBlockerBaselineMissing, reason)
		}
	} else {
		reason := "This Page has never published an application, so there is no prior application source to compare against."
		snapshot.Baseline.SourceUnavailableReason = &reason
		// Deliberately NO baseline_unavailable: an initial publication is
		// allowed. Only a MISSING retained baseline for an existing
		// publication is a refusal.
	}

	// The candidate, and the document its routines are read from.
	var comparison, comparisonChecks string
	if requested > 0 {
		spec, checks, ok := h.reviewRetainedCandidate(w, r, rec, requested, &snapshot, blocked)
		if !ok {
			return
		}
		comparison, comparisonChecks = spec, checks
	} else {
		spec, ok := h.reviewDraftCandidate(w, r, rec, publishedSource, publishedSpec, liveSpec, &snapshot, blocked)
		if !ok {
			return
		}
		comparison, comparisonChecks = spec, publishedChecks
	}
	routines, err := h.reviewRoutines(ctx, ws, comparison, comparisonChecks)
	if err != nil {
		replyInternalError(w, h.logger, "read routine definitions", err)
		return
	}
	snapshot.Routines = routines

	if h.pageArtifacts == nil || h.pageRuntimeOrigin == "" {
		blocked(reviewBlockerStorageUnavailable, "Page build storage and a separate runtime origin must be configured before an application can be published.")
	}
	if !snapshot.Capabilities.MayPublish {
		blocked(reviewBlockerNotPermitted, "Publishing an application requires Page ownership or workspace administration.")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, snapshot)
}

// reviewDraftCandidate fills in the current draft as the candidate and returns
// the document its routines are read from: the draft when there is one, and
// the live definition otherwise, so a Page with no draft still shows drift.
func (h *PageHandler) reviewDraftCandidate(w http.ResponseWriter, r *http.Request, rec *pageRecord, publishedSource, publishedSpec, liveSpec string, snapshot *reviewSnapshotWire, blocked func(string, string)) (string, bool) {
	ctx := r.Context()
	ws := WorkspaceIDFromContext(ctx)
	var draftRevision int64
	var draftDigest, draftCommit, draftSpec string
	err := h.db.QueryRowContext(ctx, `SELECT revision,source_digest,git_commit,spec_json FROM page_project_drafts WHERE page_id=?`, rec.ID).Scan(&draftRevision, &draftDigest, &draftCommit, &draftSpec)
	hasDraft := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read project draft", err)
		return "", false
	}
	comparison := draftSpec
	if !hasDraft {
		comparison = liveSpec
	}
	switch {
	case !hasDraft:
		blocked(reviewBlockerNoCandidate, "This Page has no application draft to review.")
		return comparison, true
	// Identical means BOTH bases agree: the same source bytes and the same
	// declaration. Two different sources can declare the same panels, and the
	// same source can be saved with a changed declaration.
	case publishedSource != "" && draftDigest == publishedSource && draftSpec == publishedSpec:
		blocked(reviewBlockerMatchesLive, "The current draft is identical to the live publication; there is nothing new to review.")
		return comparison, true
	}
	candidate := &reviewCandidateWire{Revision: draftRevision, GitCommit: draftCommit, SourceDigest: draftDigest}
	var actorUser, actorJSON string
	if err := h.db.QueryRowContext(ctx, `SELECT created_at,COALESCE(actor_user_id,''),actor_json FROM page_project_revisions WHERE page_id=? AND revision=?`, rec.ID, draftRevision).Scan(&candidate.CreatedAt, &actorUser, &actorJSON); err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read candidate revision", err)
		return "", false
	}
	candidate.Actor = h.reviewActor(ctx, ws, actorUser, actorJSON)
	build, ok := h.reviewBuild(ctx, rec.ID, draftRevision)
	if !ok {
		replyInternalError(w, h.logger, "read candidate build", errors.New("build lookup failed"))
		return "", false
	}
	candidate.Build = build
	snapshot.Candidate = candidate
	switch {
	case build != nil && build.State == "ready":
		// Publishable as far as the build goes.
	case build != nil && (build.State == "failed" || build.State == "interrupted"):
		blocked(reviewBlockerBuildFailed, "The build of this draft revision did not complete; build the revision again before publishing.")
	default:
		stale, err := h.reviewHasReadyBuildElsewhere(ctx, rec.ID, draftRevision)
		if err != nil {
			replyInternalError(w, h.logger, "read ready builds", err)
			return "", false
		}
		if stale {
			blocked(reviewBlockerBuildStale, "The only ready build is of an earlier source revision; build the current draft before publishing.")
		} else {
			blocked(reviewBlockerBuildMissing, "The current draft revision has no ready build.")
		}
	}
	return comparison, true
}

// reviewRetainedCandidate fills in retained publication `version` as the
// candidate — the one `rollback_version: version` would publish — and returns
// that publication's ARCHIVED spec and its recorded checks_json.
//
// The archived spec is what the publish fence recomputes routine digests from,
// so `routines[]` derived from it is exactly the key set the server will
// compare against. That equality is the whole reason this mode exists.
//
// No new blocker codes were needed. A version that is not retained is
// `no_candidate` — there is nothing to review — and a version that is already
// running is `candidate_matches_live`, which is the same statement the draft
// mode makes about a draft that is already live. `build_stale` has no meaning
// here: a retained publication names one build and there is no newer revision
// of it to be stale against.
//
// Unlike the draft mode, the candidate stays populated when it matches live.
// The caller named this version; nulling it would leave the screen unable to
// say WHICH version it is refusing, and the routines it needs are still real.
func (h *PageHandler) reviewRetainedCandidate(w http.ResponseWriter, r *http.Request, rec *pageRecord, version int64, snapshot *reviewSnapshotWire, blocked func(string, string)) (string, string, bool) {
	ctx := r.Context()
	ws := WorkspaceIDFromContext(ctx)
	candidate := &reviewCandidateWire{}
	var buildID, spec, checks, actorUser string
	err := h.db.QueryRowContext(ctx, `SELECT build_id,source_revision,source_digest,git_commit,spec_json,checks_json,COALESCE(actor_user_id,''),created_at FROM page_project_publications WHERE page_id=? AND version=?`, rec.ID, version).
		Scan(&buildID, &candidate.Revision, &candidate.SourceDigest, &candidate.GitCommit, &spec, &checks, &actorUser, &candidate.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		blocked(reviewBlockerNoCandidate, fmt.Sprintf("Publication %d is not retained for this Page; read the publication history for the versions that are.", version))
		return "", "", true
	}
	if err != nil {
		replyInternalError(w, h.logger, "read retained publication", err)
		return "", "", false
	}
	// The actor a publication recorded is its PUBLISHER, not the author of the
	// source revision, and the column is cleared when that person is erased.
	// An erased publisher is `unknown`; borrowing the revision's author would
	// answer a different question than the field asks.
	candidate.Actor = h.reviewActor(ctx, ws, actorUser, "")
	var build reviewBuildWire
	build.ID = buildID
	err = h.db.QueryRowContext(ctx, `SELECT state,COALESCE(artifact_digest,''),error FROM page_project_builds WHERE id=?`, buildID).Scan(&build.State, &build.ArtifactDigest, &build.Error)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Publishing verifies the recorded build, so a compacted one refuses.
		blocked(reviewBlockerBuildMissing, fmt.Sprintf("The build publication %d recorded is no longer retained, so this version cannot be republished.", version))
	case err != nil:
		replyInternalError(w, h.logger, "read retained publication build", err)
		return "", "", false
	default:
		candidate.Build = &build
		if build.State != "ready" {
			blocked(reviewBlockerBuildFailed, fmt.Sprintf("The build publication %d recorded is %s, not ready, so this version cannot be republished.", version, build.State))
		}
	}
	if snapshot.Baseline.Published && snapshot.Baseline.PublicationVersion == version {
		blocked(reviewBlockerMatchesLive, fmt.Sprintf("Publication %d is already the live application; there is nothing to restore.", version))
	}
	snapshot.Candidate = candidate
	return spec, checks, true
}

// reviewBaselineSource answers whether the live publication's retained source
// still reads back, through the same ReadCheckpoint path publish verifies with.
//
// Anything short of "reads back and matches the recorded declaration" is
// unavailable. A checkpoint that returns different bytes is worse than a
// missing one, and both produce a diff basis that would be a lie.
func (h *PageHandler) reviewBaselineSource(ctx context.Context, ws, page, commit, publishedSpec string) (bool, *string) {
	reason := func(text string) (bool, *string) { return false, &text }
	if commit == "" {
		return reason("The live publication predates Git checkpoints, so its source cannot be read back for comparison.")
	}
	if h.projectStore == nil {
		return reason("Page project storage is not configured on this server, so the published source cannot be read back for comparison.")
	}
	source, archived, err := h.projectStore.ReadCheckpoint(ctx, ws, page, commit)
	if err != nil {
		return reason("The retained source for the live publication (checkpoint " + commit + ") could not be read back: " + err.Error())
	}
	if _, err := source.Digest(); err != nil {
		return reason("The retained source for the live publication (checkpoint " + commit + ") failed integrity verification: " + err.Error())
	}
	if archived != publishedSpec {
		return reason("The retained source for the live publication (checkpoint " + commit + ") carries a different Page definition than the publication recorded.")
	}
	return true, nil
}

// reviewBuild returns the most recent build of exactly this revision, or nil.
func (h *PageHandler) reviewBuild(ctx context.Context, page string, revision int64) (*reviewBuildWire, bool) {
	var b reviewBuildWire
	err := h.db.QueryRowContext(ctx, `SELECT id,state,COALESCE(artifact_digest,''),error FROM page_project_builds WHERE page_id=? AND source_revision=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, page, revision).Scan(&b.ID, &b.State, &b.ArtifactDigest, &b.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	return &b, true
}

func (h *PageHandler) reviewHasReadyBuildElsewhere(ctx context.Context, page string, revision int64) (bool, error) {
	var count int
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_project_builds WHERE page_id=? AND state='ready' AND source_revision<>?`, page, revision).Scan(&count)
	return count > 0, err
}

// reviewRoutines compares the routine definitions this publication recorded
// with the ones that would run now.
//
// `unknown` covers both gaps: the publication recorded no digest for this
// routine, and the routine no longer resolves in this workspace. Neither is
// evidence of agreement, and rendering either as `unchanged` is the defect
// this state exists to prevent.
func (h *PageHandler) reviewRoutines(ctx context.Context, ws, specJSON, checksJSON string) ([]reviewRoutineWire, error) {
	published := map[string]string{}
	if checksJSON != "" {
		var checks pageRoutineChecks
		if err := json.Unmarshal([]byte(checksJSON), &checks); err == nil {
			for name, digest := range checks.Definitions {
				published[name] = digest
			}
		}
	}
	declared := map[string]bool{}
	if specJSON != "" {
		var doc pages.Document
		if err := json.Unmarshal([]byte(specJSON), &doc); err == nil {
			for _, panel := range doc.Spec.Panels {
				for _, action := range panel.Actions {
					if action.Kind == pages.ActionCall && action.Routine != "" {
						declared[action.Routine] = true
					}
				}
			}
		}
	}
	names := make([]string, 0, len(published)+len(declared))
	seen := map[string]bool{}
	for name := range published {
		if !seen[name] {
			seen[name], names = true, append(names, name)
		}
	}
	for name := range declared {
		if !seen[name] {
			seen[name], names = true, append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]reviewRoutineWire, 0, len(names))
	for _, name := range names {
		row := reviewRoutineWire{Routine: name, State: "unknown"}
		if digest, ok := published[name]; ok && digest != "" {
			value := digest
			row.PublishedDigest = &value
		}
		var definition string
		err := h.db.QueryRowContext(ctx, `SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, ws, name).Scan(&definition)
		if err == nil {
			value := pageRoutineDigest(definition)
			row.CurrentDigest = &value
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if row.PublishedDigest != nil && row.CurrentDigest != nil {
			if *row.PublishedDigest == *row.CurrentDigest {
				row.State = "unchanged"
			} else {
				row.State = "changed"
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// reviewActor turns the stored id snapshot into a kind and an id, and resolves
// a label only when a real current row answers for it.
func (h *PageHandler) reviewActor(ctx context.Context, ws, actorUser, actorJSON string) reviewActorWire {
	var stored struct {
		UserID      string `json:"user_id"`
		WorkspaceID string `json:"workspace_id"`
		CrewID      string `json:"crew_id"`
		AgentID     string `json:"agent_id"`
		OwnerCrewID string `json:"owner_crew_id"`
	}
	if actorJSON != "" {
		_ = json.Unmarshal([]byte(actorJSON), &stored)
	}
	lookup := func(query string, args ...any) string {
		var label string
		if err := h.db.QueryRowContext(ctx, query, args...).Scan(&label); err != nil {
			return ""
		}
		return label
	}
	switch {
	case stored.UserID != "" || actorUser != "":
		id := stored.UserID
		if id == "" {
			id = actorUser
		}
		return reviewActorWire{Kind: "user", ID: id, Label: lookup(`SELECT COALESCE(email,'') FROM users WHERE id=?`, id)}
	case stored.AgentID != "":
		return reviewActorWire{Kind: "agent", ID: stored.AgentID, Label: lookup(`SELECT COALESCE(slug,'') FROM agents WHERE id=? AND workspace_id=?`, stored.AgentID, ws)}
	case stored.CrewID != "":
		return reviewActorWire{Kind: "crew", ID: stored.CrewID, Label: lookup(`SELECT COALESCE(slug,'') FROM crews WHERE id=? AND workspace_id=?`, stored.CrewID, ws)}
	}
	return reviewActorWire{Kind: "unknown", ID: ""}
}
