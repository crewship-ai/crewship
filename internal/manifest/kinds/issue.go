// Package kinds — kind: Issue.
//
// This file implements the standalone `kind: Issue` document used by
// the declarative manifest pipeline to author/update individual issues
// (Linear-style tickets) outside the wrapping `kind: Crew` or
// `kind: RecurringIssue` shapes. The cousin kind RecurringIssue in
// this directory mints the same shape on a cron; Issue is the per-row
// CRUD entry point for human-authored, one-shot tickets that should
// be tracked declaratively in source control.
//
// REST surface this kind uses
// ---------------------------
//
//	POST   /api/v1/crews/{crewId}/issues                — issues.Create (CREW-scoped)
//	GET    /api/v1/issues                                — issues.List (WORKSPACE-scoped)
//	GET    /api/v1/crews/{crewId}/issues/{identifier}    — issues.Get
//	PATCH  /api/v1/crews/{crewId}/issues/{identifier}    — issues.Update
//	DELETE /api/v1/crews/{crewId}/issues/{identifier}    — issues.Delete
//	GET    /api/v1/crews                                 — to resolve spec.crew_slug → crew_id
//	GET    /api/v1/projects                              — to resolve spec.project_slug → project_id
//	GET    /api/v1/agents                                — to resolve spec.assignee_slug → agent_id
//	GET    /api/v1/labels                                — to resolve spec.labels (slugs) → label_ids
//
// SLUG ↔ IDENTIFIER MAPPING (design note, read this before changing
// the lookup helper!)
// -------------------------------------------------------------------
//
// The `missions` table has NO `slug` column. The server-generated
// identifier (e.g. "ENG-7") is the natural per-crew key but it's
// assigned by an atomic counter on POST — the manifest can't pin or
// predict it. The manifest's `metadata.slug` is therefore purely a
// MANIFEST-SIDE idempotency key. It serves two purposes:
//
//   1. Cross-document FK references inside one bundle (a Project doc
//      could one day reference a "must-do" issue slug; nothing does
//      today, but the field stays for symmetry with every other kind).
//   2. The plan-line and journal entry use the slug so re-applies
//      produce stable, grep-able output.
//
// Drift detection matches a declared issue to a remote row by the
// pair (crew_id, title). Title is the only stable user-authored
// field on the row — the manifest cannot use identifier (server-
// generated), and assignee/priority/status drift constantly via the
// UI so they're poor identity candidates. The trade-off:
//
//   - PRO: zero schema change, no side table, idempotent across
//     re-applies as long as the title doesn't drift.
//   - CON: renaming the issue title in the manifest creates a new
//     row instead of updating the old one. This is symmetric to how
//     Milestone behaves (also no slug column) and operators are
//     warned via the per-kind doc.
//
// A future v2 of the schema could add `missions.manifest_slug` and
// the lookup would switch to slug-keyed in one place — the helper
// LookupIssueRemoteBySlug encapsulates the strategy so the call
// sites in Plan/Export stay stable across that change.
//
// All non-exported helpers carry an `issue` prefix to avoid the
// "which helper wins" puzzle when this package compiles. The shared
// helpers in helpers.go are intentionally left alone — each kind
// owns its own checkStatus/readAll variant under a stable prefix so
// future edits stay narrowly scoped.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Constants ───────────────────────────────────────────────────────────────

// issueAPIVersion is the only apiVersion this kind accepts. Future
// versions get their own constant + parse fork so we never silently
// downgrade a v2 manifest to v1 semantics.
const issueAPIVersion = "crewship/v1"

// issueKind is the literal `kind:` value used in YAML envelopes and
// recorded on PlanItem.Kind for CLI plan output.
const issueKind = "Issue"

// defaultIssuePriority mirrors the server-side default applied by
// issue_handler_create.go when priority == "". We re-apply the same
// default here so the manifest's diff logic sees a non-empty value
// and doesn't repeatedly try to "fix" a server row that was created
// with the implicit default.
const defaultIssuePriority = "none"

// ── Enums ───────────────────────────────────────────────────────────────────
//
// Mirror the corresponding allow-lists in internal/api and
// internal/statuses. Validating here means a typo in priority/status
// fails at `crewship plan` time without an HTTP round-trip, and the
// error message can spell out the allowed values for the user.
//
// "none" lives in the priority enum because the schema default is
// 'none' — a manifest that omits priority round-trips as "none" so
// we must accept it as a legal value on the way back in. The task
// spec calls out urgent/high/medium/low but the server set is
// broader; we accept the broader server set for forward-compat.

var validIssuePriorities = map[string]struct{}{
	"none":   {},
	"low":    {},
	"medium": {},
	"high":   {},
	"urgent": {},
}

// validIssueStatuses is the closed set of starting statuses an Issue
// document may declare. The full transition graph lives in
// internal/statuses/transitions.go — the manifest only validates the
// declared status sits inside the universe; the server itself
// enforces transition legality on PATCH. We accept the canonical
// uppercase form AND tolerate lowercase ("backlog") because the
// task-spec example uses lowercase. The toCreateBody helper
// up-cases before sending so the server always sees BACKLOG/TODO/…
var validIssueStatuses = map[string]string{
	// declared → canonical (server form)
	"backlog":     "BACKLOG",
	"todo":        "TODO",
	"in_progress": "IN_PROGRESS",
	"review":      "REVIEW",
	"done":        "DONE",
	"failed":      "FAILED",
	"cancelled":   "CANCELLED",
	"BACKLOG":     "BACKLOG",
	"TODO":        "TODO",
	"IN_PROGRESS": "IN_PROGRESS",
	"REVIEW":      "REVIEW",
	"DONE":        "DONE",
	"FAILED":      "FAILED",
	"CANCELLED":   "CANCELLED",
}

// ── Types ───────────────────────────────────────────────────────────────────

// IssueSpec is the shape under `spec:` for kind: Issue.
//
// Mirrors the create handler's request shape from
// internal/api/issue_handler_create.go where field names align.
// Slug-FKs (crew_slug, project_slug, assignee_slug, labels) are
// resolved to IDs by Plan; everything else maps 1:1 onto the body.
type IssueSpec struct {
	// CrewSlug is the parent crew. REQUIRED — issues are crew-scoped
	// (the create endpoint embeds {crewId} in the URL). Resolved to a
	// crew_id at Plan time via GET /api/v1/crews.
	CrewSlug string `yaml:"crew_slug" json:"crew_slug"`

	// Title is the human-facing title. Optional in the YAML — if
	// blank the kind falls back to metadata.name. The server requires
	// a non-empty title on create; toCreateBody enforces the fallback.
	Title string `yaml:"title,omitempty" json:"title,omitempty"`

	// Description is the free-form markdown body. Empty allowed.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Priority is the urgency tier. One of none | low | medium |
	// high | urgent (the server's closed set; the task spec calls
	// out the subset urgent/high/medium/low). Empty means "let the
	// server default to none".
	Priority string `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Status is the starting status. Optional; the server defaults
	// to BACKLOG on create. When set on Update, the server validates
	// the transition is legal from the current status.
	Status string `yaml:"status,omitempty" json:"status,omitempty"`

	// AssigneeSlug is the agent that owns the issue. Optional;
	// resolved to (assignee_type=agent, assignee_id=<id>) at Plan
	// time. Pre-task-spec name was `assignee_id` but the manifest
	// uses slugs everywhere else for cross-workspace portability.
	AssigneeSlug string `yaml:"assignee_slug,omitempty" json:"assignee_slug,omitempty"`

	// ProjectSlug attaches the issue to a project. Optional;
	// resolved to project_id at Plan time.
	ProjectSlug string `yaml:"project_slug,omitempty" json:"project_slug,omitempty"`

	// Labels is a list of label slugs/names (Label kind enforces
	// slug == name) to attach. The Create handler accepts an array
	// of label IDs in `labels`; Plan resolves slug → id before POST.
	Labels []string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// IssueDocument is the YAML envelope produced/consumed by the
// manifest pipeline. metadata.slug is the workspace-unique manifest-
// side idempotency key — see the SLUG ↔ IDENTIFIER MAPPING block at
// the top of this file for why it's NOT persisted server-side.
type IssueDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       IssueSpec            `yaml:"spec"       json:"spec"`
}

// IssueRemote is the slice of GET /api/v1/issues each row produces —
// only the fields the manifest needs to diff or round-trip through
// export. Fields like sort_order, comment_count, timestamps stay out
// of the plan because they're pure runtime state.
//
// CrewID / ProjectID / AssigneeID are the resolved CUIDs; Export
// maps them back to slugs via parallel /api/v1/crews + /projects +
// /agents fetches so the exported IssueDocument carries stable,
// re-applyable references.
type IssueRemote struct {
	ID           string  `json:"id"`
	WorkspaceID  string  `json:"workspace_id"`
	CrewID       string  `json:"crew_id"`
	CrewSlug     string  `json:"crew_slug"`
	Identifier   *string `json:"identifier"`
	Title        string  `json:"title"`
	Description  *string `json:"description"`
	Status       string  `json:"status"`
	Priority     string  `json:"priority"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	ProjectID    *string `json:"project_id"`
	// Labels arrives as a denormalised array; we only need each
	// label's name for round-trip (Label kind keys on name).
	Labels []issueRemoteLabel `json:"labels"`
}

// issueRemoteLabel is the trimmed shape of the labelResponse the
// issues list endpoint denormalises into each row. We only need ID
// and Name; color/group are display concerns.
type issueRemoteLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// issueCrewStub is the minimal shape this kind needs from
// GET /api/v1/crews to resolve crew_slug → crew_id. Defined locally
// so the file stays self-contained; cross-kind imports inside the
// kinds package create initialisation-ordering surprises and break
// test isolation.
type issueCrewStub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// issueProjectStub is the minimal shape from GET /api/v1/projects
// used to resolve project_slug → project_id.
type issueProjectStub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// issueAgentStub is the minimal shape from GET /api/v1/agents used
// to resolve assignee_slug → agent_id.
type issueAgentStub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// issueLabelStub is the minimal shape from GET /api/v1/labels used
// to resolve label slugs to IDs. Labels have no slug column server-
// side; the Label kind keeps slug == name so we accept either as
// the lookup key.
type issueLabelStub struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ── Validate ────────────────────────────────────────────────────────────────

// Validate enforces the structural rules without any HTTP round-trip.
// Required: metadata.slug, spec.crew_slug, and either spec.title or
// metadata.name (one must yield a non-empty title). Enum fields are
// checked against their allow-lists when set. FK references
// (crew_slug, project_slug, assignee_slug, labels) are checked
// against `wsCtx` when the corresponding declared/remote slice is
// populated — Validate is offline-tolerant and degrades gracefully
// when wsCtx is empty (Plan will fail at the resolution step).
func (d *IssueDocument) Validate(wsCtx internalapi.WorkspaceContext) error {
	if d.APIVersion != "" && d.APIVersion != issueAPIVersion {
		return fmt.Errorf("issue %q: apiVersion %q must be %q",
			d.Metadata.Slug, d.APIVersion, issueAPIVersion)
	}
	if d.Kind != "" && d.Kind != issueKind {
		return fmt.Errorf("issue %q: kind %q must be %q",
			d.Metadata.Slug, d.Kind, issueKind)
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("issue: metadata.slug is required")
	}
	if strings.TrimSpace(d.Spec.CrewSlug) == "" {
		// crew_slug is required by the API (the create endpoint is
		// crew-scoped via {crewId} in the URL). Catching it at
		// Validate time means the operator sees the missing field
		// in the plan output instead of getting a confused 404
		// during Apply.
		return fmt.Errorf("issue %q: spec.crew_slug is required", d.Metadata.Slug)
	}

	// Title fallback: metadata.name is the human-readable display
	// name; spec.title is the on-the-row title. When the YAML only
	// declares one of the two we treat them as the same string.
	// Validate ensures at least one is non-empty so the server's
	// "title is required" 400 can't fire at Apply time.
	if d.resolvedTitle() == "" {
		return fmt.Errorf("issue %q: spec.title (or metadata.name) is required", d.Metadata.Slug)
	}

	if d.Spec.Priority != "" {
		if _, ok := validIssuePriorities[d.Spec.Priority]; !ok {
			return fmt.Errorf("issue %q: invalid priority %q (want one of none|low|medium|high|urgent)",
				d.Metadata.Slug, d.Spec.Priority)
		}
	}
	if d.Spec.Status != "" {
		if _, ok := validIssueStatuses[d.Spec.Status]; !ok {
			return fmt.Errorf("issue %q: invalid status %q (want one of backlog|todo|in_progress|review|done|failed|cancelled)",
				d.Metadata.Slug, d.Spec.Status)
		}
	}

	// FK checks against the workspace context. Each missing slug is
	// reported with a distinct error so the author knows which
	// reference to fix. Empty wsCtx slices = "skip" (Validate runs
	// in isolation for unit tests; Plan will catch the dangling FK
	// when it tries to resolve).
	if (len(wsCtx.DeclaredCrews) > 0 || len(wsCtx.RemoteCrews) > 0) && !wsCtx.HasCrew(d.Spec.CrewSlug) {
		return fmt.Errorf("issue %q: spec.crew_slug %q does not reference any declared or remote crew",
			d.Metadata.Slug, d.Spec.CrewSlug)
	}
	if d.Spec.ProjectSlug != "" &&
		(len(wsCtx.DeclaredProjects) > 0 || len(wsCtx.RemoteProjects) > 0) &&
		!wsCtx.HasProject(d.Spec.ProjectSlug) {
		return fmt.Errorf("issue %q: spec.project_slug %q does not reference any declared or remote project",
			d.Metadata.Slug, d.Spec.ProjectSlug)
	}
	if d.Spec.AssigneeSlug != "" &&
		(len(wsCtx.DeclaredAgents) > 0 || len(wsCtx.RemoteAgents) > 0) &&
		!wsCtx.HasAgent(d.Spec.AssigneeSlug) {
		return fmt.Errorf("issue %q: spec.assignee_slug %q does not reference any declared or remote agent",
			d.Metadata.Slug, d.Spec.AssigneeSlug)
	}
	if len(wsCtx.DeclaredLabels) > 0 || len(wsCtx.RemoteLabels) > 0 {
		for i, lbl := range d.Spec.Labels {
			if strings.TrimSpace(lbl) == "" {
				return fmt.Errorf("issue %q: spec.labels[%d] is empty", d.Metadata.Slug, i)
			}
			if !wsCtx.HasLabel(lbl) {
				return fmt.Errorf("issue %q: spec.labels references unknown label %q",
					d.Metadata.Slug, lbl)
			}
		}
	} else {
		// Even without label context, reject empty entries — they're
		// always a typo. Slug presence checking just degrades.
		for i, lbl := range d.Spec.Labels {
			if strings.TrimSpace(lbl) == "" {
				return fmt.Errorf("issue %q: spec.labels[%d] is empty", d.Metadata.Slug, i)
			}
		}
	}

	return nil
}

// resolvedTitle returns spec.title, or metadata.name when title is
// empty. The server requires a non-empty title on POST; this is the
// one place that decides which YAML field wins.
func (d *IssueDocument) resolvedTitle() string {
	if t := strings.TrimSpace(d.Spec.Title); t != "" {
		return t
	}
	return strings.TrimSpace(d.Metadata.Name)
}

// ── Plan ────────────────────────────────────────────────────────────────────

// Plan compares the declared document against `remote` (nil = not yet
// on server) and returns the plan items the apply loop should
// execute. A single PlanItem is returned even for the Unchanged case
// so the CLI can count "0 changed, 1 unchanged" truthfully.
//
// The Exec closure on a Create item performs the FK resolution then
// POSTs to the crew-scoped endpoint. The Update Exec computes a
// sparse PATCH body containing only drifted fields.
//
// Labels are passed as an ID array in the POST body and as an
// explicit "labels" replacement in PATCH (the Update handler treats
// a non-nil labels field as a full set replacement).
func (d *IssueDocument) Plan(ctx context.Context, c internalapi.Client, remote *IssueRemote) ([]internalapi.PlanItem, error) {
	crewID, err := issueLookupCrewIDBySlug(ctx, c, d.Spec.CrewSlug)
	if err != nil {
		return nil, fmt.Errorf("issue %q: resolve crew_slug: %w", d.Metadata.Slug, err)
	}

	if remote == nil {
		// CREATE — resolve all FKs once, then POST. We pre-resolve
		// at Plan time rather than inside Exec so the dry-run path
		// surfaces a dangling FK before we touch the wire. Resolution
		// errors propagate up so the operator sees which slug is
		// unknown before Apply makes any mutation.
		projectID, err := issueResolveOptionalProjectID(ctx, c, d.Spec.ProjectSlug)
		if err != nil {
			return nil, fmt.Errorf("issue %q: resolve project_slug: %w", d.Metadata.Slug, err)
		}
		assigneeID, err := issueResolveOptionalAgentID(ctx, c, d.Spec.AssigneeSlug)
		if err != nil {
			return nil, fmt.Errorf("issue %q: resolve assignee_slug: %w", d.Metadata.Slug, err)
		}
		labelIDs, err := issueResolveLabelIDs(ctx, c, d.Spec.Labels)
		if err != nil {
			return nil, fmt.Errorf("issue %q: resolve labels: %w", d.Metadata.Slug, err)
		}

		body := d.toCreateBody(projectID, assigneeID, labelIDs)
		slug := d.Metadata.Slug
		title := d.resolvedTitle()

		return []internalapi.PlanItem{{
			Kind:        "issue",
			Slug:        slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create issue %q in crew %q", title, d.Spec.CrewSlug),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				path := fmt.Sprintf("/api/v1/crews/%s/issues", crewID)
				resp, err := c.Post(ctx, path, body)
				if err != nil {
					return fmt.Errorf("POST %s: %w", path, err)
				}
				return issueCheckStatus(resp, "create issue "+slug)
			},
		}}, nil
	}

	// UPDATE / UNCHANGED — resolve only the FKs we need to diff
	// against the remote. Labels are compared by name (the Label
	// kind enforces slug == name) so we don't need ID resolution
	// for the diff itself; we DO need it for the PATCH body.
	projectID, err := issueResolveOptionalProjectID(ctx, c, d.Spec.ProjectSlug)
	if err != nil {
		return nil, fmt.Errorf("issue %q: resolve project_slug: %w", d.Metadata.Slug, err)
	}
	assigneeID, err := issueResolveOptionalAgentID(ctx, c, d.Spec.AssigneeSlug)
	if err != nil {
		return nil, fmt.Errorf("issue %q: resolve assignee_slug: %w", d.Metadata.Slug, err)
	}

	patch, labelsChanged, err := d.diffPatch(remote, projectID, assigneeID)
	if err != nil {
		return nil, fmt.Errorf("issue %q: diff: %w", d.Metadata.Slug, err)
	}

	identifier := ""
	if remote.Identifier != nil {
		identifier = *remote.Identifier
	}
	slug := d.Metadata.Slug
	remoteCrewID := remote.CrewID
	if remoteCrewID == "" {
		// Defensive fallback — the issues list endpoint always
		// populates crew_id but a future schema change could relax
		// that. Use the resolved crew_id from the declared slug.
		remoteCrewID = crewID
	}

	if len(patch) == 0 && !labelsChanged {
		return []internalapi.PlanItem{{
			Kind:        "issue",
			Slug:        slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("issue %q already matches manifest", slug),
		}}, nil
	}

	// Labels go in the same PATCH body as the field diff — the
	// Update handler treats a non-nil "labels" key as a full
	// replacement of the join rows. Resolve once at Plan time so
	// the dry-run path surfaces a dangling label slug.
	if labelsChanged {
		labelIDs, err := issueResolveLabelIDs(ctx, c, d.Spec.Labels)
		if err != nil {
			return nil, fmt.Errorf("issue %q: resolve labels: %w", d.Metadata.Slug, err)
		}
		// Even an empty manifest list must be sent (operator wants
		// to clear). The PATCH reader unmarshals a present empty
		// array as []string{} which triggers the delete-all branch.
		if labelIDs == nil {
			labelIDs = []string{}
		}
		patch["labels"] = labelIDs
	}

	return []internalapi.PlanItem{{
		Kind:        "issue",
		Slug:        slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update issue %q (%d field(s))", slug, len(patch)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			if identifier == "" {
				return fmt.Errorf("issue %q: remote row has no identifier; cannot PATCH", slug)
			}
			path := fmt.Sprintf("/api/v1/crews/%s/issues/%s", remoteCrewID, identifier)
			resp, err := c.Patch(ctx, path, patch)
			if err != nil {
				return fmt.Errorf("PATCH %s: %w", path, err)
			}
			return issueCheckStatus(resp, "update issue "+slug)
		},
	}}, nil
}

// ── Body builders ───────────────────────────────────────────────────────────

// toCreateBody renders the POST /api/v1/crews/{crewId}/issues body.
// The crew_id is already in the URL path so the body itself omits
// it. Optional fields are only included when set so the server
// applies its own defaults (priority="none", status="BACKLOG").
//
// Status on create is interesting: the create handler hard-codes
// status='BACKLOG'. The manifest can still declare a status, but
// the handler ignores it on POST — to land the row in a non-default
// state, Plan would have to emit a Create followed by an Update.
// We're not doing that yet; if a user declares status:done on a
// brand-new Issue document the row lands in BACKLOG and the next
// Plan run will detect drift and PATCH it to DONE. This is a known
// two-apply quirk and is documented in the per-kind docs page.
func (d *IssueDocument) toCreateBody(projectID, assigneeID string, labelIDs []string) map[string]any {
	body := map[string]any{
		"title": d.resolvedTitle(),
	}
	if d.Spec.Description != "" {
		body["description"] = d.Spec.Description
	}
	priority := d.Spec.Priority
	if priority == "" {
		priority = defaultIssuePriority
	}
	body["priority"] = priority
	if assigneeID != "" {
		body["assignee_type"] = "agent"
		body["assignee_id"] = assigneeID
	}
	if projectID != "" {
		body["project_id"] = projectID
	}
	if len(labelIDs) > 0 {
		body["labels"] = labelIDs
	}
	return body
}

// diffPatch returns the sparse PATCH body containing ONLY the fields
// whose declared value differs from `remote`. Empty declared fields
// are skipped — they mean "leave server value alone" — so a manifest
// that omits priority won't blank out a priority a UI user set after
// the initial apply.
//
// Returns (patch, labelsChanged, error). labelsChanged is a separate
// signal because the Plan caller is responsible for resolving label
// slugs to IDs before stuffing them into the patch — keeps the
// concern of "did the set drift" separate from "do we have IDs to
// send".
func (d *IssueDocument) diffPatch(remote *IssueRemote, projectID, assigneeID string) (map[string]any, bool, error) {
	patch := map[string]any{}

	if t := d.resolvedTitle(); t != "" && t != remote.Title {
		patch["title"] = t
	}
	if d.Spec.Description != "" && d.Spec.Description != issueDerefOrEmpty(remote.Description) {
		patch["description"] = d.Spec.Description
	}
	if d.Spec.Priority != "" && d.Spec.Priority != remote.Priority {
		patch["priority"] = d.Spec.Priority
	}
	if d.Spec.Status != "" {
		canonical := validIssueStatuses[d.Spec.Status]
		if canonical != "" && canonical != remote.Status {
			patch["status"] = canonical
		}
	}
	// Assignee diff: only emit when the manifest declares one.
	// Clearing an assignee via manifest (assignee_slug:"") would
	// require sending assignee_id="" but the server treats that as
	// "set to empty string", not "clear". We document the
	// limitation; clearing is a UI operation today.
	if assigneeID != "" {
		remoteAssigneeID := issueDerefOrEmpty(remote.AssigneeID)
		remoteAssigneeType := issueDerefOrEmpty(remote.AssigneeType)
		if remoteAssigneeID != assigneeID || remoteAssigneeType != "agent" {
			patch["assignee_type"] = "agent"
			patch["assignee_id"] = assigneeID
		}
	}
	if projectID != "" && projectID != issueDerefOrEmpty(remote.ProjectID) {
		patch["project_id"] = projectID
	}

	// Labels drift is set-based: a declared []string compared to the
	// remote denormalised label rows. We compare on name (Label kind
	// enforces slug == name).
	labelsChanged := !issueSameLabelSet(d.Spec.Labels, remote.Labels)

	return patch, labelsChanged, nil
}

// issueSameLabelSet returns true when the declared label slugs and
// the remote label names form the same set (order-insensitive). Both
// inputs are deduped and lowercased? No — names are case-sensitive
// in the server, so we compare verbatim. A manifest declaring an
// empty list while the remote has labels DOES drift (we need to
// emit a clear).
func issueSameLabelSet(declared []string, remote []issueRemoteLabel) bool {
	if len(declared) != len(remote) {
		return false
	}
	d := append([]string{}, declared...)
	sort.Strings(d)
	r := make([]string, 0, len(remote))
	for _, lbl := range remote {
		r = append(r, lbl.Name)
	}
	sort.Strings(r)
	for i := range d {
		if d[i] != r[i] {
			return false
		}
	}
	return true
}

// ── Lookup helpers ──────────────────────────────────────────────────────────

// LookupIssueRemoteBySlug fetches the live state of one issue by
// MANIFEST slug. As explained in the SLUG ↔ IDENTIFIER MAPPING block
// at the top of this file, the missions table has no slug column —
// we match remote rows to a declared manifest doc by (crew_slug,
// title) instead. The function name keeps "BySlug" for symmetry with
// LookupAgentRemoteBySlug so the apply pipeline's per-kind call
// sites stay uniform; the implementation is what it has to be.
//
// Returns (nil, nil) when no row matches the (crew, title) pair —
// Plan treats that as ActionCreate. We pull only the one crew's
// issues to keep the scan bounded; the workspace-level GET
// /api/v1/issues would also work but loads all crews.
func LookupIssueRemoteBySlug(ctx context.Context, c internalapi.Client, slug, crewSlug, title string) (*IssueRemote, error) {
	crewID, err := issueLookupCrewIDBySlug(ctx, c, crewSlug)
	if err != nil {
		// Crew missing → issue obviously absent. Surface the
		// underlying error so callers can choose between waiting
		// for the crew to be created earlier in the bundle and
		// aborting.
		return nil, err
	}
	rows, err := issueListForCrew(ctx, c, crewID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Title == title {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// LookupCrewIDBySlug is exported because the per-kind resolver
// pattern keeps it discoverable from callers in this package's
// sibling files (apply_kinds.go, plan.go) that may want a
// single canonical helper. We have LookupCrewIDBySlug in agent.go
// already — to avoid duplicate-symbol errors at link time we use
// the agent-prefixed unexported variant internally and DON'T
// re-export. Callers that need the canonical helper should use
// the one in agent.go.

// issueLookupCrewIDBySlug resolves a crew slug to its CUID. Returns
// a not-found error when no row matches; the caller decorates with
// "issue %q:" context. We deliberately use an unexported, prefixed
// name so we don't collide with LookupCrewIDBySlug already exported
// from agent.go (Go would otherwise emit a duplicate-symbol error).
func issueLookupCrewIDBySlug(ctx context.Context, c internalapi.Client, slug string) (string, error) {
	if strings.TrimSpace(slug) == "" {
		return "", fmt.Errorf("crew slug is empty")
	}
	crews, err := issueListCrews(ctx, c)
	if err != nil {
		return "", err
	}
	for _, cr := range crews {
		if cr.Slug == slug {
			return cr.ID, nil
		}
	}
	return "", fmt.Errorf("crew with slug %q not found", slug)
}

// issueResolveOptionalProjectID returns "" + nil when slug is empty
// (optional FK — caller skips the patch field) and either the
// resolved ID or a not-found error when slug is non-empty.
func issueResolveOptionalProjectID(ctx context.Context, c internalapi.Client, slug string) (string, error) {
	if strings.TrimSpace(slug) == "" {
		return "", nil
	}
	projects, err := issueListProjects(ctx, c)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.Slug == slug {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("project with slug %q not found", slug)
}

// issueResolveOptionalAgentID is the agent-slug analogue of
// issueResolveOptionalProjectID.
func issueResolveOptionalAgentID(ctx context.Context, c internalapi.Client, slug string) (string, error) {
	if strings.TrimSpace(slug) == "" {
		return "", nil
	}
	agents, err := issueListAgents(ctx, c)
	if err != nil {
		return "", err
	}
	for _, a := range agents {
		if a.Slug == slug {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("agent with slug %q not found", slug)
}

// issueResolveLabelIDs maps each declared slug (== name) to a label
// ID via one GET /api/v1/labels. Returns an error mentioning the
// offending slug on the first miss so the user can fix the typo
// without rerunning Plan to find more.
func issueResolveLabelIDs(ctx context.Context, c internalapi.Client, slugs []string) ([]string, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	labels, err := issueListLabels(ctx, c)
	if err != nil {
		return nil, err
	}
	idByName := make(map[string]string, len(labels))
	for _, l := range labels {
		idByName[l.Name] = l.ID
	}
	out := make([]string, 0, len(slugs))
	for _, s := range slugs {
		id, ok := idByName[s]
		if !ok {
			return nil, fmt.Errorf("label %q not found in workspace", s)
		}
		out = append(out, id)
	}
	// Sort so the POST body is deterministic — keeps assertion
	// tests and the issue.labels join rows stable across applies.
	sort.Strings(out)
	return out, nil
}

// ── Export ──────────────────────────────────────────────────────────────────

// ExportIssues iterates every crew in the workspace and lists its
// issues, rendering each as an IssueDocument suitable for re-applying.
// The function is the inverse of Plan/Create — fields the manifest
// doesn't model (identifier, timestamps, comment_count) are dropped.
//
// We list per-crew rather than calling GET /api/v1/issues because
// the workspace endpoint denormalises crew_slug onto each row and
// the per-crew loop keeps the resolution map symmetrical with how
// Plan looks the data up. With < a few hundred crews per workspace
// the extra round-trips are acceptable for an explicit operator
// action.
func ExportIssues(ctx context.Context, c internalapi.Client) ([]*IssueDocument, error) {
	crews, err := issueListCrews(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export issues: list crews: %w", err)
	}
	projects, err := issueListProjects(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export issues: list projects: %w", err)
	}
	agents, err := issueListAgents(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export issues: list agents: %w", err)
	}

	projectSlugByID := make(map[string]string, len(projects))
	for _, p := range projects {
		projectSlugByID[p.ID] = p.Slug
	}
	agentSlugByID := make(map[string]string, len(agents))
	for _, a := range agents {
		agentSlugByID[a.ID] = a.Slug
	}

	var out []*IssueDocument
	for _, crew := range crews {
		rows, err := issueListForCrew(ctx, c, crew.ID)
		if err != nil {
			return nil, fmt.Errorf("export issues for crew %q: %w", crew.Slug, err)
		}
		for _, row := range rows {
			doc := &IssueDocument{
				APIVersion: issueAPIVersion,
				Kind:       issueKind,
				Metadata: internalapi.Metadata{
					Name: row.Title,
					// Namespace the manifest slug by crew so two
					// crews can both have a "Ping check" without
					// emitting duplicate metadata.slug values. The
					// title-slug derivation falls back to a
					// stripped identifier when the title contains
					// no slug-safe characters.
					Slug: crew.Slug + "--" + issueSlugFromTitle(row.Title, row.Identifier),
				},
				Spec: IssueSpec{
					CrewSlug:    crew.Slug,
					Title:       row.Title,
					Description: issueDerefOrEmpty(row.Description),
					Priority:    row.Priority,
					Status:      row.Status,
				},
			}
			if row.ProjectID != nil {
				if slug, ok := projectSlugByID[*row.ProjectID]; ok {
					doc.Spec.ProjectSlug = slug
				}
			}
			if row.AssigneeID != nil && issueDerefOrEmpty(row.AssigneeType) == "agent" {
				if slug, ok := agentSlugByID[*row.AssigneeID]; ok {
					doc.Spec.AssigneeSlug = slug
				}
			}
			if len(row.Labels) > 0 {
				labels := make([]string, 0, len(row.Labels))
				for _, l := range row.Labels {
					if l.Name != "" {
						labels = append(labels, l.Name)
					}
				}
				sort.Strings(labels)
				doc.Spec.Labels = labels
			}
			out = append(out, doc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Slug < out[j].Metadata.Slug })
	return out, nil
}

// issueSlugFromTitle produces a kebab-case slug for round-trip
// identity on export. Falls back to the server identifier (e.g.
// "ENG-7") when the title yields an empty slug — a row entirely
// composed of emoji/punctuation would otherwise have no anchor.
func issueSlugFromTitle(title string, identifier *string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		if identifier != nil && *identifier != "" {
			return strings.ToLower(*identifier)
		}
		return "issue"
	}
	return out
}

// ── HTTP helpers (all issue-prefixed) ───────────────────────────────────────

// issueListCrews pulls /api/v1/crews and decodes the minimal shape
// we need for slug↔id round-tripping.
func issueListCrews(ctx context.Context, c internalapi.Client) ([]issueCrewStub, error) {
	resp, err := c.Get(ctx, "/api/v1/crews")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/crews: %w", err)
	}
	if err := issueCheckStatus(resp, "list crews"); err != nil {
		return nil, err
	}
	body, err := issueReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/crews body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []issueCrewStub
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/crews: %w", err)
	}
	return rows, nil
}

// issueListProjects pulls /api/v1/projects and decodes the minimal
// shape this kind needs.
func issueListProjects(ctx context.Context, c internalapi.Client) ([]issueProjectStub, error) {
	resp, err := c.Get(ctx, "/api/v1/projects")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/projects: %w", err)
	}
	if err := issueCheckStatus(resp, "list projects"); err != nil {
		return nil, err
	}
	body, err := issueReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/projects body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []issueProjectStub
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/projects: %w", err)
	}
	return rows, nil
}

// issueListAgents pulls /api/v1/agents and decodes the minimal shape.
func issueListAgents(ctx context.Context, c internalapi.Client) ([]issueAgentStub, error) {
	resp, err := c.Get(ctx, "/api/v1/agents")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/agents: %w", err)
	}
	if err := issueCheckStatus(resp, "list agents"); err != nil {
		return nil, err
	}
	body, err := issueReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/agents body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []issueAgentStub
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/agents: %w", err)
	}
	return rows, nil
}

// issueListLabels pulls /api/v1/labels (workspace-scoped). Labels
// have no slug column server-side; the manifest treats name as the
// key (Label kind enforces slug == name).
func issueListLabels(ctx context.Context, c internalapi.Client) ([]issueLabelStub, error) {
	resp, err := c.Get(ctx, "/api/v1/labels")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/labels: %w", err)
	}
	if err := issueCheckStatus(resp, "list labels"); err != nil {
		return nil, err
	}
	body, err := issueReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/labels body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []issueLabelStub
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/labels: %w", err)
	}
	return rows, nil
}

// issueListForCrew fetches every issue under one crew by hitting the
// workspace-scoped GET /api/v1/issues with ?crew_id=<id>. The
// dedicated crew-scoped GET endpoint exists for single-row reads
// (/api/v1/crews/{crewId}/issues/{identifier}) but the list-by-crew
// surface is the workspace endpoint with a query filter — see the
// List handler in issue_handler_crud.go.
func issueListForCrew(ctx context.Context, c internalapi.Client, crewID string) ([]IssueRemote, error) {
	// limit=100 matches the server's max page size; if a single
	// crew has more than 100 issues we'd silently truncate. The
	// alternative (looped pagination) doubles complexity for a
	// case that the manifest pipeline isn't expected to hit (a
	// crew with 100+ declarative issues is a misuse). Documented;
	// not implemented.
	path := fmt.Sprintf("/api/v1/issues?crew_id=%s&limit=100", crewID)
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if err := issueCheckStatus(resp, "list issues for crew"); err != nil {
		return nil, err
	}
	body, err := issueReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", path, err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []IssueRemote
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return rows, nil
}

// issueDerefOrEmpty unboxes a *string into its value or "" for nil.
// The issues REST API returns sql.NullString-style pointers for
// nullable text columns; the manifest treats absent and "" as
// equivalent for diffing purposes.
func issueDerefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// issueCheckStatus mirrors agentCheckStatus / milestoneCheckStatus —
// duplicated here under an issue-prefix to avoid the cross-file
// "which package-local helper wins" puzzle that would arise from
// reusing the shared helpers.checkStatus across kinds that need
// slightly different error wrapping. Reads up to 4 KiB of the body
// into the error message so the server's RFC 7807 Problem Details
// reach the CLI.
func issueCheckStatus(resp *internalapi.Response, op string) error {
	if resp == nil {
		return fmt.Errorf("%s: nil response", op)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet := ""
	if resp.Body != nil {
		if b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10)); err == nil && len(b) > 0 {
			snippet = ": " + strings.TrimSpace(string(b))
		}
	}
	return fmt.Errorf("%s: HTTP %d%s", op, resp.StatusCode, snippet)
}

// issueReadAll consumes a Response body and returns the bytes;
// tolerates nil so test mocks can omit Body for not-stubbed paths.
func issueReadAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}
