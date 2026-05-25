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

import "net/http"

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
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
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
	{
		ID:         "remember",
		Label:      "Remember this",
		LabelCS:    "Zapamatuj si toto",
		Icon:       "brain",
		Capability: CapabilityMemoryWrite,
		FormSchema: []slashFormField{
			{Name: "content", Type: "textarea", Required: true},
			{Name: "scope", Type: "memory_scope", Default: "agent"},
		},
	},
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
}

// NewSlashCommandsHandler captures the router so we can reach
// r.db (for CapabilitiesForMember) without piping it through a
// separate field. Same shape as InternalHandler.
func NewSlashCommandsHandler(r *Router) *SlashCommandsHandler {
	return &SlashCommandsHandler{r: r}
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
	caps, _, ok := CapabilitiesForMember(r.Context(), h.r.db, wsID, user.ID)
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
	writeJSON(w, http.StatusOK, out)
}
