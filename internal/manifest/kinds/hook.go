// Package kinds holds the per-kind implementations for the declarative
// manifest pipeline. Each kind owns its own *.go file plus a paired
// _test.go and documentation page under docs/manifest.
//
// This file implements `kind: Hook` — the smallest manifest kind in
// the system. Hooks are registered in CODE (not over REST): an
// operator can NEVER create a new hook via the manifest. The manifest
// pipeline knows how to do exactly one thing with a hook: flip the
// `enabled` boolean on or off via the existing
// POST /api/v1/hooks/{id}/{enable|disable} endpoints.
//
// Because the create / delete verbs are intentionally absent, Plan
// produces only ActionUnchanged or ActionUpdate items, never
// ActionCreate / ActionDelete. ApplyReplace mode collapses to the
// default upsert path because "replace" has no meaning for a registry
// the user cannot author.
//
// The slug<->id contract: the hooks table has no `slug` column. The
// manifest treats the hook's `id` (the CUID returned by
// /api/v1/hooks) as the slug. Operators register hooks in code with a
// stable ID; the manifest references that ID via metadata.slug so
// cross-environment apply is reproducible. The handler endpoint path
// embeds that same ID after `/api/v1/hooks/`.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Constants ───────────────────────────────────────────────────────────────

// hookAPIVersion is the only apiVersion value Hook documents accept.
// Future versions get their own constant + parse fork so a manifest
// authored against a newer-than-supported schema fails loud instead of
// being silently downgraded.
const hookAPIVersion = "crewship/v1"

// hookKind is the literal `kind:` value used in YAML and reported on
// PlanItem.Kind for CLI output.
const hookKind = "Hook"

// ── Types ───────────────────────────────────────────────────────────────────

// HookSpec is the shape under `spec:` for kind: Hook. The entire spec
// surface is intentionally a single boolean: hooks cannot be created
// or rewritten via the manifest, only toggled. Anything richer (event,
// matcher, handler config) would imply create authority that the API
// deliberately withholds.
type HookSpec struct {
	// Enabled is the desired runtime state. When this differs from the
	// hook's current `enabled` column on the server, Plan emits an
	// ActionUpdate that POSTs the matching enable/disable endpoint.
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// HookDocument is the top-level envelope for kind: Hook. metadata.slug
// MUST be the hook's stable id (the value returned in `id` from
// GET /api/v1/hooks) because that's the path segment the toggle
// endpoints consume.
type HookDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       HookSpec             `yaml:"spec"       json:"spec"`
}

// HookRemote is the projected slice of a server-side hooks_config row
// the manifest pipeline cares about. We deliberately drop every field
// Plan does not diff on (matcher, handler_config, timestamps): the
// manifest is toggle-only, so the wire-type stays small.
//
// Description is synthesised from `event + handler_kind` because the
// hooks_config table has no description column. The synthesised string
// is purely for human-readable plan output ("toggle hook X — pre_run
// shell hook"); diffing never reads it.
type HookRemote struct {
	// ID is the stable, code-registered hook id. Used as the path
	// segment for /api/v1/hooks/{id}/enable.
	ID string `json:"id"`

	// Slug mirrors ID. Kept as a distinct field so callers can treat
	// HookRemote symmetrically with other *Remote types whose slug and
	// id come from different DB columns.
	Slug string `json:"slug"`

	// Enabled is the live state. Plan compares this to spec.Enabled.
	Enabled bool `json:"enabled"`

	// Description is a synthesised one-liner for CLI output. Empty for
	// hooks the server returned without an event/handler_kind (should
	// not happen in practice).
	Description string `json:"description,omitempty"`
}

// hookListEnvelope mirrors the wrapped shape the /api/v1/hooks handler
// emits: `{"rows": [...], "count": N}`. We model the wrapper so
// decoding never silently picks up an unrelated top-level field on a
// future API change.
type hookListEnvelope struct {
	Rows  []hookListRow `json:"rows"`
	Count int           `json:"count"`
}

// hookListRow captures the subset of the handler's hookRow we need to
// build HookRemote. Fields the toggle-only manifest never touches
// (matcher, handler_config, timestamps, created_by, blocking) are
// omitted so a future schema tweak on the unrelated fields cannot
// break the manifest pipeline.
type hookListRow struct {
	ID          string `json:"id"`
	Event       string `json:"event"`
	HandlerKind string `json:"handler_kind"`
	Enabled     bool   `json:"enabled"`
}

// ── Validate ────────────────────────────────────────────────────────────────

// Validate enforces only the structural rules a static check can
// verify. The "hook exists on the server" rule lives in Plan because
// it requires a live Client. This split mirrors Project / Milestone:
// Validate catches manifest authoring bugs; Plan catches drift.
//
// workspaceCtx is unused — hooks have no FK references to other kinds
// — but the parameter is kept on the signature so the validate-phase
// dispatcher can call every kind through one uniform method.
func (d *HookDocument) Validate(_ internalapi.WorkspaceContext) error {
	if d.APIVersion != hookAPIVersion {
		return fmt.Errorf("hook %q: unsupported apiVersion %q (want %q)",
			d.Metadata.Slug, d.APIVersion, hookAPIVersion)
	}
	if d.Kind != hookKind {
		return fmt.Errorf("hook %q: kind must be %q, got %q",
			d.Metadata.Slug, hookKind, d.Kind)
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("hook: metadata.slug is required (must match a hook id registered in code)")
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("hook %q: metadata.name is required", d.Metadata.Slug)
	}
	return nil
}

// ── Plan ────────────────────────────────────────────────────────────────────

// Plan diffs the declared toggle state against the remote registry
// and emits at most one PlanItem.
//
// Possible outcomes:
//
//   - hook not registered remotely → ActionUpdate with a non-nil Exec
//     that returns a "register it in code first" error. Encoding the
//     failure as a plan item (rather than aborting Plan with err) lets
//     dry-run surface every missing hook in one pass instead of
//     stopping at the first one.
//   - declared.enabled == remote.enabled → ActionUnchanged.
//   - drift → ActionUpdate that POSTs /enable or /disable.
//
// Plan never emits ActionCreate or ActionDelete: hooks are not
// user-creatable via REST. ApplyReplace callers receive the same plan
// items as the default mode.
//
// `remote` is passed by the caller (apply.go's dispatcher), which is
// expected to have already fetched the registry via ListHooks below.
// `remote == nil` means "the dispatcher could not find a hook with
// this slug on the server" — i.e. the hook is not registered.
func (d *HookDocument) Plan(_ context.Context, _ internalapi.Client, remote *HookRemote) ([]internalapi.PlanItem, error) {
	slug := d.Metadata.Slug

	if remote == nil {
		// Hook missing from the registry. Surface as an erroring plan
		// item so dry-run reports the issue and Apply fails when the
		// closure runs. The Exec closure is what carries the error so
		// `crewship apply --dry-run` can print every missing hook in
		// the same pass.
		msg := fmt.Sprintf(
			"hook %q is not registered — register it in code first",
			slug,
		)
		return []internalapi.PlanItem{{
			Kind:        hookKind,
			Slug:        slug,
			Action:      internalapi.ActionUpdate,
			Description: msg,
			Exec: func(context.Context, internalapi.Client) error {
				return fmt.Errorf("%s", msg)
			},
		}}, nil
	}

	// Already in the desired state.
	if remote.Enabled == d.Spec.Enabled {
		return []internalapi.PlanItem{{
			Kind:        hookKind,
			Slug:        slug,
			Action:      internalapi.ActionUnchanged,
			Description: hookUnchangedDescription(d, remote),
		}}, nil
	}

	// Toggle. Pick enable vs disable based on the declared state.
	verb := "disable"
	if d.Spec.Enabled {
		verb = "enable"
	}
	id := remote.ID
	path := fmt.Sprintf("/api/v1/hooks/%s/%s", id, verb)
	desc := fmt.Sprintf("%s hook %q", verb, slug)
	if remote.Description != "" {
		desc = fmt.Sprintf("%s hook %q (%s)", verb, slug, remote.Description)
	}

	return []internalapi.PlanItem{{
		Kind:        hookKind,
		Slug:        slug,
		Action:      internalapi.ActionUpdate,
		Description: desc,
		Exec: func(ctx context.Context, c internalapi.Client) error {
			resp, err := c.Post(ctx, path, map[string]any{})
			if err != nil {
				return fmt.Errorf("POST %s: %w", path, err)
			}
			return expectHookSuccess(resp, verb+" hook "+slug)
		},
	}}, nil
}

// hookUnchangedDescription builds the human-readable line for the
// no-op case. We surface the current state in the message so dry-run
// output ("hook 'pre-run-cost-gate' already enabled") is more useful
// than a generic "unchanged".
func hookUnchangedDescription(d *HookDocument, remote *HookRemote) string {
	state := "disabled"
	if d.Spec.Enabled {
		state = "enabled"
	}
	if remote.Description != "" {
		return fmt.Sprintf("hook %q already %s (%s)", d.Metadata.Slug, state, remote.Description)
	}
	return fmt.Sprintf("hook %q already %s", d.Metadata.Slug, state)
}

// ── Export ──────────────────────────────────────────────────────────────────

// ExportHooks fetches every hook visible to the workspace and renders
// each as a HookDocument suitable for re-applying. Because the user
// cannot author new hooks via the manifest, the round-trip property
// here is one-way: `export → apply` is a no-op (every hook is already
// in the desired state because the manifest just reflects the server),
// but it lets operators capture the current toggle layout for
// version control.
//
// The output slug is the hook's `id` (matching what Plan expects in
// metadata.slug). Description is synthesised from event + handler kind
// because the hooks_config table has no description column.
func ExportHooks(ctx context.Context, c internalapi.Client) ([]*HookDocument, error) {
	remotes, err := ListHooks(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]*HookDocument, 0, len(remotes))
	for _, r := range remotes {
		doc := &HookDocument{
			APIVersion: hookAPIVersion,
			Kind:       hookKind,
			Metadata: internalapi.Metadata{
				Name:        r.Slug,
				Slug:        r.Slug,
				Description: r.Description,
			},
			Spec: HookSpec{Enabled: r.Enabled},
		}
		out = append(out, doc)
	}
	return out, nil
}

// ── Public helpers (used by apply.go's dispatcher) ──────────────────────────

// ListHooks fetches the full hooks registry for the active workspace.
// Used by both ExportHooks (round-trip) and apply.go (to build the
// remote-state map keyed by slug before invoking Plan on each
// declared HookDocument).
func ListHooks(ctx context.Context, c internalapi.Client) ([]HookRemote, error) {
	resp, err := c.Get(ctx, "/api/v1/hooks")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/hooks: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("GET /api/v1/hooks: nil response")
	}
	if resp.StatusCode/100 != 2 {
		body := readSnippet(resp.Body)
		return nil, fmt.Errorf("GET /api/v1/hooks: HTTP %d: %s", resp.StatusCode, body)
	}
	data, err := readAllHook(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/hooks body: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}

	// Accept both the documented wrapped shape (`{rows: [...], count: N}`)
	// and a bare array for forward-compat against a future un-wrap.
	var envelope hookListEnvelope
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Rows != nil {
		return hookRowsToRemote(envelope.Rows), nil
	}
	var flat []hookListRow
	if err := json.Unmarshal(data, &flat); err == nil {
		return hookRowsToRemote(flat), nil
	}
	return nil, fmt.Errorf("decode /api/v1/hooks: unrecognized response shape")
}

// FindHookBySlug walks the hooks list and returns the first row whose
// id matches the given slug. Returns (nil, nil) when no hook matches —
// callers (apply.go) treat that as "not registered" and feed it into
// Plan as a nil remote, which then emits the "register in code first"
// error item.
func FindHookBySlug(ctx context.Context, c internalapi.Client, slug string) (*HookRemote, error) {
	rows, err := ListHooks(ctx, c)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Slug == slug {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// ── Internal helpers ────────────────────────────────────────────────────────

// hookRowsToRemote converts wire-rows into HookRemote values, folding
// the synthesised description in one place so wrapped/flat decoders
// don't drift.
func hookRowsToRemote(rows []hookListRow) []HookRemote {
	out := make([]HookRemote, 0, len(rows))
	for _, r := range rows {
		out = append(out, HookRemote{
			ID:          r.ID,
			Slug:        r.ID,
			Enabled:     r.Enabled,
			Description: hookDescription(r),
		})
	}
	return out
}

// hookDescription builds a stable one-line summary from the registry
// fields the API exposes. Used purely for human-readable plan output;
// never compared during drift detection.
//
// Format: "<event> <handler_kind> hook"
// Examples: "pre_run shell hook", "post_run http hook"
//
// Returns "" when both event and handler_kind are blank (defensive —
// the server always emits them, but a malformed row shouldn't crash
// the export path).
func hookDescription(r hookListRow) string {
	event := strings.TrimSpace(r.Event)
	kind := strings.TrimSpace(r.HandlerKind)
	switch {
	case event != "" && kind != "":
		return fmt.Sprintf("%s %s hook", event, kind)
	case event != "":
		return fmt.Sprintf("%s hook", event)
	case kind != "":
		return fmt.Sprintf("%s hook", kind)
	default:
		return ""
	}
}

// expectHookSuccess maps a non-2xx response to an error carrying the
// body snippet so apply's CLI output surfaces the server's RFC 7807
// Problem Details. Mirrors the pattern in project.go / milestone.go
// but stays local so this kind compiles without cross-kind helper
// dependencies.
func expectHookSuccess(resp *internalapi.Response, op string) error {
	if resp == nil {
		return fmt.Errorf("%s: nil response", op)
	}
	if resp.StatusCode/100 == 2 {
		return nil
	}
	body := readSnippet(resp.Body)
	if body == "" {
		return fmt.Errorf("%s: HTTP %d", op, resp.StatusCode)
	}
	return fmt.Errorf("%s: HTTP %d: %s", op, resp.StatusCode, body)
}

// readSnippet pulls the first 4 KB of a response body for error
// messages. Tolerates a nil reader (common in tests) and trims
// trailing whitespace so multi-line server errors render cleanly.
func readSnippet(r io.Reader) string {
	if r == nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	return strings.TrimSpace(string(b))
}

// readAllHook reads the response body up to 10 MB. Hooks lists are
// tiny (typically < 50 rows) so this is a defensive cap, not a tuning
// knob. Tolerates nil readers for test mocks that synthesise headers
// without bodies.
func readAllHook(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r, 10<<20))
}
