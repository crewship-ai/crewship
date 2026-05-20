// Package internalapi holds the interfaces and value types that the
// per-kind packages (internal/manifest/kinds/*) depend on. Keeping
// these here breaks an import cycle: top-level manifest needs to import
// kinds to wire dispatch; kinds need types from manifest to declare
// method signatures. Pulling the shared shapes into a third leaf
// package resolves the cycle without leaking implementation detail.
//
// Concrete implementations of these interfaces live in the parent
// `internal/manifest` package (Client adapter, Bundle, etc.).
package internalapi

import (
	"context"
	"io"
)

// Metadata is the descriptive header common to every kind. Slug is the
// workspace-unique idempotency key; the rest is human-facing.
type Metadata struct {
	Name        string            `yaml:"name"                  json:"name"`
	Slug        string            `yaml:"slug"                  json:"slug"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"      json:"labels,omitempty"`
}

// PlanAction is the verb a single plan item performs against the
// remote workspace. Mirrors apply.go's existing Action ints so the
// Plan/Apply pipeline can route on it.
type PlanAction int

const (
	ActionUnchanged PlanAction = iota
	ActionCreate
	ActionUpdate
	ActionDelete
)

// String returns a stable lowercase verb for use in CLI plan output.
func (a PlanAction) String() string {
	switch a {
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	default:
		return "unchanged"
	}
}

// PlanItem is one unit of work produced by a kind's Plan() method. The
// Exec closure is nil for ActionUnchanged. Apply walks plan items in
// the order Plan returned them; per-phase ordering is enforced by the
// top-level apply.go which calls each kind's Plan in phase order.
type PlanItem struct {
	// Kind is the lowercased kind name (e.g. "project", "label"). Used
	// in CLI output and journal entries; the apply loop never branches
	// on this.
	Kind string

	// Slug identifies the entity within its kind for human-readable
	// plan output. For nested entities (routine.schedule), use a dotted
	// form like "discord-sync.hourly".
	Slug string

	// Action drives summary counters and confirmation prompts. Delete
	// counts as destructive.
	Action PlanAction

	// Description is a one-line summary shown in `apply --dry-run`
	// output and in the destructive-action confirmation prompt.
	Description string

	// Exec runs the mutation. nil for ActionUnchanged. Receives the
	// same client + options the top-level Apply got.
	Exec func(ctx context.Context, c Client) error
}

// Response is the minimal HTTP response shape kinds need. Wrapping it
// here (rather than reusing *http.Response directly) lets the manifest
// package layer in retries / logging without leaking transport detail
// into per-kind code.
type Response struct {
	StatusCode int
	Body       io.Reader
}

// Client is the REST surface every kind uses. The concrete implementation
// lives in internal/manifest/client.go and adapts the existing
// *manifest.Client to this interface via a thin shim.
//
// All paths are absolute (e.g. "/api/v1/projects"). Body is marshalled to
// JSON by the implementation; pass nil for verbs that don't take a body.
type Client interface {
	Get(ctx context.Context, path string) (*Response, error)
	Post(ctx context.Context, path string, body any) (*Response, error)
	Patch(ctx context.Context, path string, body any) (*Response, error)
	Put(ctx context.Context, path string, body any) (*Response, error)
	Delete(ctx context.Context, path string) (*Response, error)

	// WorkspaceID returns the slug-or-id of the workspace this apply is
	// targeting. Used by kinds whose endpoints embed the workspace
	// (e.g. /api/v1/workspaces/{wsId}/pipelines).
	WorkspaceID() string
}

// SlugLookup is a thin reference used by WorkspaceContext for FK
// validation. Name is included so validators can produce
// human-readable error messages ("project 'Q2 Roadmap' (slug=q2-roadmap)
// not found").
type SlugLookup struct {
	Slug string
	Name string
}

// WorkspaceContext gives a kind's Validate() method visibility into
// what OTHER kinds the manifest declares + what the server already
// has. Slug-FK validation walks these slices; missing references
// produce a structured error so the user sees the unresolved slug.
//
// Builders populate this in two passes: first from the manifest
// (Declared* slices), then from `GET` calls against the server
// (Remote* slices) for cases where a kind references something that
// might not be in the same manifest (e.g. RecurringIssue references a
// crew that was created in a previous apply).
type WorkspaceContext struct {
	// Declared* are slugs of entities present in the current manifest.
	DeclaredProjects   []SlugLookup
	DeclaredLabels     []SlugLookup
	DeclaredMilestones []SlugLookup
	DeclaredCrews      []SlugLookup
	DeclaredAgents     []SlugLookup
	DeclaredRoutines   []SlugLookup

	// Remote* are slugs of entities the server already has. Populated
	// lazily by the manifest package's validate phase; nil if the
	// fetcher hasn't run yet (in which case Validate skips remote FK
	// checks rather than erroring — Apply will surface mismatches).
	RemoteProjects []SlugLookup
	RemoteLabels   []SlugLookup
	RemoteCrews    []SlugLookup
	RemoteAgents   []SlugLookup
}

// HasProject reports whether the slug is present in either the
// declared or remote project list. Helpers below cover the common
// cross-kind FK shapes — kinds use these instead of walking the
// slices themselves so a future change to lookup strategy (e.g. add
// case-insensitive matching) lives in one place.
func (w *WorkspaceContext) HasProject(slug string) bool {
	return hasSlug(w.DeclaredProjects, slug) || hasSlug(w.RemoteProjects, slug)
}

// HasLabel reports whether the label slug is declared or remote.
func (w *WorkspaceContext) HasLabel(slug string) bool {
	return hasSlug(w.DeclaredLabels, slug) || hasSlug(w.RemoteLabels, slug)
}

// HasCrew reports whether the crew slug is declared or remote.
func (w *WorkspaceContext) HasCrew(slug string) bool {
	return hasSlug(w.DeclaredCrews, slug) || hasSlug(w.RemoteCrews, slug)
}

// HasAgent reports whether the agent slug is declared or remote.
func (w *WorkspaceContext) HasAgent(slug string) bool {
	return hasSlug(w.DeclaredAgents, slug) || hasSlug(w.RemoteAgents, slug)
}

func hasSlug(list []SlugLookup, slug string) bool {
	for _, item := range list {
		if item.Slug == slug {
			return true
		}
	}
	return false
}
