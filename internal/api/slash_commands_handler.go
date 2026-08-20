package api

// GET /api/v1/slash-commands — server-driven slash command registry
// (PRD-SLASH-CAPABILITIES-2026 §6.6).
//
// The registry feeds both surfaces:
//
//   - Chat UI palette (components/features/chat/composer/slash-palette.tsx
//     — commit 7 extends with an "actions" group fed by this endpoint)
//   - CLI repl autocomplete (internal/cli/repl.go — commit 8 merges
//     these with the file-based ~/.crewship/commands/*.md catalog)
//
// Each entry carries a `capability` field. The handler intersects the
// catalog with the caller's capability set (cached lookup via
// CapabilitiesForMember) and returns only the entries the caller is
// allowed to invoke. Entries the caller can't use never appear on the
// wire — UI doesn't have to render-then-hide, CLI doesn't have to
// tab-complete-then-error.
//
// i18n: catalog entries carry both `label` (en) and `label_cs` (cs)
// so the dashboard can pick by user locale without a translation
// step. The shape is open — adding `label_de`, `label_es`, ... later
// is a non-breaking field addition.

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// slashCommand is one entry in the static catalog the handler
// returns. Field names match the JSON wire shape — Title-case Go
// names with json tags would be fine too but the JSON shape here
// is small enough to inline.
type slashCommand struct {
	ID         string           `json:"id"`
	Label      string           `json:"label"`
	LabelCS    string           `json:"label_cs,omitempty"`
	Icon       string           `json:"icon,omitempty"`
	Capability string           `json:"capability"`
	FormSchema []slashFormField `json:"form_schema,omitempty"`
}

// slashFormField describes one form field the slash action modal
// renders. Type names are an open set; the dashboard renderer falls
// back to "text" for unknown types so adding a new type here doesn't
// require coordinated UI rollout.
type slashFormField struct {
	Name string `json:"name"`
	// Type is the WIDGET to draw: text, textarea, cron, timezone,
	// secret, slug, priority, number, boolean, … It answers "what does
	// the user see", not "what does the server receive".
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	// Default is a string because a form field's value is a string. A
	// non-string default from a routine's input spec is formatted into
	// one here and parsed back on the way out — see ValueType.
	Default string `json:"default,omitempty"`
	// ValueType is the JSON type the SERVER expects back for this field:
	// string | integer | number | boolean | array | object. Empty means
	// string, which is what every entry in the static catalog is.
	//
	// It exists because Type cannot answer the question. A routine's
	// `array` input and an issue's `description` both draw a textarea,
	// and one of them has to reach the server as a parsed JSON array
	// while the other must not. Inferring the difference from the widget
	// worked only as long as no two data types shared one — a property
	// no widget vocabulary keeps for long. So the wire says both.
	//
	// Optional field addition: a client that ignores it sends strings,
	// which is exactly what it sent before this field existed.
	ValueType string `json:"value_type,omitempty"`
	// Help is rendered under the field. Carries a routine input's
	// `description`, which is the one place its author gets to say what
	// the value means ("YYYY-MM; empty means the previous month") — the
	// static catalog's fields have none and omit it.
	Help string `json:"help,omitempty"`
}

// slashCommandCatalog is the static platform-defined registry. Order
// in the slice is the order returned to the client (CLI + UI both
// render in slice order) — keep groupings logical (high-stakes
// actions like credential.create later in the list).
//
// Adding a new slash command: append here + ensure the capability
// constant exists in capabilities.go + ensure a backend handler
// gates on the same capability. No other wire-up needed.
var slashCommandCatalog = []slashCommand{
	{
		ID:         "routine",
		Label:      "Create routine from this conversation",
		LabelCS:    "Vytvořit rutinu z této konverzace",
		Icon:       "calendar-clock",
		Capability: CapabilityRoutineCreate,
		FormSchema: []slashFormField{
			{Name: "name", Type: "text", Required: true},
			{Name: "cron", Type: "cron", Required: true},
			{Name: "timezone", Type: "timezone", Default: "UTC"},
		},
	},
	{
		ID:         "issue",
		Label:      "Create issue from this conversation",
		LabelCS:    "Vytvořit issue z této konverzace",
		Icon:       "alert-circle",
		Capability: CapabilityIssueCreate,
		FormSchema: []slashFormField{
			{Name: "title", Type: "text", Required: true},
			{Name: "description", Type: "textarea"},
			{Name: "priority", Type: "priority", Default: "none"},
		},
	},
	// "remember" slash command is intentionally NOT in the catalog
	// yet — the `/api/v1/memory/write` backend route doesn't exist
	// (memory write happens via the sidecar's loopback /memory/write,
	// which the dashboard / public CLI can't reach). Tracked as a
	// follow-up PR: add a public memory write endpoint that runs the
	// HITL verifier from MEMORY-ROADMAP-2026 PR #3 and registers
	// here. The CapabilityMemoryWrite constant + role bundles stay
	// so the follow-up only needs to add this catalog entry +
	// endpoint without a schema change.
	{
		ID:         "skill",
		Label:      "Create skill from this conversation",
		LabelCS:    "Vytvořit skill z této konverzace",
		Icon:       "sparkles",
		Capability: CapabilitySkillCreate,
		FormSchema: []slashFormField{
			{Name: "slug", Type: "slug", Required: true},
			{Name: "prompt", Type: "textarea", Required: true},
		},
	},
	{
		ID:         "credential",
		Label:      "Add credential",
		LabelCS:    "Přidat credential",
		Icon:       "key",
		Capability: CapabilityCredentialCreate,
		FormSchema: []slashFormField{
			{Name: "name", Type: "text", Required: true},
			{Name: "type", Type: "credential_type", Default: "SECRET"},
			{Name: "value", Type: "secret", Required: true},
		},
	},
}

// SlashCommandsHandler is a thin GET handler. Construction is
// dependency-free aside from *sql.DB for the membership lookup, so
// router wire-up is a one-liner.
type SlashCommandsHandler struct {
	r *Router
	// pipelines reads the workspace's routines for the per-routine half
	// of the catalog (slash_routine_catalog.go). Its own store rather
	// than a reach into r.PipelinesHandler: this handler wants one
	// read-only List and nothing else the run handler carries, and the
	// store is a struct around the same *sql.DB. nil → the catalog is
	// the static list alone, which is what a test that builds this
	// handler without a DB gets.
	pipelines *pipeline.Store
}

// NewSlashCommandsHandler captures the router so we can reach
// r.db (for CapabilitiesForMember) without piping it through a
// separate field. Same shape as InternalHandler.
func NewSlashCommandsHandler(r *Router) *SlashCommandsHandler {
	h := &SlashCommandsHandler{r: r}
	if r != nil && r.db != nil {
		h.pipelines = pipeline.NewStore(r.db)
	}
	return h
}

// List handles GET /api/v1/slash-commands. Returns the catalog
// intersected with the caller's capability set. JWT-authed (via the
// `authed` middleware in registerRoutes); the caller's id and the
// active workspace context drive the filter.
//
// Empty capability set (caller has only "chat") returns an empty
// array, not a 403 — slash palette opens, just has no actions in it.
// That matches the UX expectation: a user without grants still sees
// the chat palette's view / tools / navigation groups, just no
// actions group.
func (h *SlashCommandsHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		// Shouldn't happen on authed routes, but defence-in-depth:
		// without a caller id we have nothing to filter by, and
		// returning the full catalog would be a capability bypass.
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		// Slash palette is workspace-scoped — without a workspace
		// the filter has no meaning. UI sends ?workspace_id=... or
		// uses the wsCtx middleware path; either populates the ctx.
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	caps, role, ok := CapabilitiesForMember(r.Context(), h.r.db, wsID, user.ID)
	if !ok {
		// Caller isn't a member of the workspace they asked about.
		// Empty list is the least-surprise response — same shape as
		// "all capabilities revoked" — rather than 403 which the UI
		// would have to special-case.
		writeJSON(w, http.StatusOK, []slashCommand{})
		return
	}
	out := make([]slashCommand, 0, len(slashCommandCatalog))
	for _, sc := range slashCommandCatalog {
		if HasCapability(caps, sc.Capability) {
			out = append(out, sc)
		}
	}
	if canRunRoutines(caps, role) {
		out = append(out, h.routineSlashCommands(r.Context(), wsID)...)
	}
	writeJSON(w, http.StatusOK, out)
}

// canRunRoutines reports whether the caller may invoke a saved routine,
// and therefore whether the per-routine entries belong in their palette.
//
// It is the run endpoint's admission rule, restated for the catalog:
// role at MANAGER+ OR an explicit routine.run grant (see
// PipelineHandler.Run). The palette must not offer what the endpoint
// refuses — an entry the caller can't run is a button that 403s.
//
// The converse is narrower and deliberately so: routineSlashCommands
// lists only workspace-visible, non-ephemeral routines, while the run
// endpoint will happily run a hidden or ephemeral one addressed by slug.
// A routine marked not-visible staying out of a palette is what
// not-visible means; the asymmetry is on the routine SET, never on who
// is admitted.
//
// The role half is not redundant with the capability half. Migration
// v109 backfilled every membership's capability column before
// routine.run existed, so a workspace ADMIN whose row that migration
// wrote holds routine.create and not routine.run — while clearing the
// role gate on the endpoint without either. Filtering on the capability
// alone would have hidden the palette from the very people who
// administer it, and done so only on databases old enough to have been
// backfilled, which is every real one.
func canRunRoutines(caps map[string]struct{}, role string) bool {
	return canRole(role, "create") || HasCapability(caps, CapabilityRoutineRun)
}
