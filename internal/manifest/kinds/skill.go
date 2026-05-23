// Package kinds holds the per-kind implementations for the declarative
// manifest pipeline. This file implements `kind: Skill` — a top-level
// document that imports a SKILL.md body into the workspace's skill
// registry.
//
// Why a dedicated kind (and not just the nested Skill struct under
// Crew/Workspace in schema.go) ?
// -----------------------------------------------------------------
//
// The existing `Skill` type in internal/manifest/schema.go is a
// per-crew attachment — it expresses "this crew's agents may use this
// skill" by ID after the skill row already exists. The standalone
// `kind: Skill` document is the inverse: it expresses "ensure THIS
// SKILL.md exists in the registry" and is independent of any crew.
// Manifests in the wild typically pair the two: declare the Skill once
// at the top level, then reference its slug from any number of crews.
//
// REST surface used by this kind
// ------------------------------
//
//	GET   /api/v1/skills                                       — list
//	POST  /api/v1/workspaces/{workspaceId}/skills/import       — create OR update (upsert, keyed on slug)
//	DELETE /api/v1/workspaces/{workspaceId}/skills/{skillId}   — delete (not used by Plan; sync-mode only)
//
// The Import endpoint is genuinely an upsert (see
// internal/skills/importer.go:upsertEnriched) so a single mutation
// covers both Create and Update. We still distinguish the two actions
// in PlanItem so dry-run output reads correctly ("create skill X" vs
// "update skill X") and Apply's per-action counters stay meaningful.
//
// Body sources
// ------------
//
// The spec accepts exactly one of three mutually-exclusive sources:
//
//   - `inline:`  — the SKILL.md body embedded verbatim. Capped at 8 KiB
//     so a manifest stays diff-friendly; anything larger
//     should live in a sibling file (path:).
//   - `path:`    — a relative path to a SKILL.md file resolved against
//     the manifest file's directory. The bundle loader
//     in internal/manifest/parse.go resolves the path
//     (with the same `safeJoin` sandbox that the nested
//     Skill type uses) and stuffs the body into the
//     unexported `resolved` field BEFORE Validate runs.
//     SkillDocument.Resolved()/SetResolved() are the
//     accessors. Validate intentionally does NOT read
//     the file itself — keeping Validate offline lets
//     `crewship validate` run without an apparent file
//     dependency surprise (the bundle loader already
//     reported any read error long before Validate is
//     reached).
//   - `source:`  — an HTTPS URL to a remote SKILL.md. The import
//     handler does the fetch itself (with SSRF guard +
//     license gate); the manifest layer just forwards
//     the URL.
//
// Following the same convention as the nested Skill: at most one
// source may be set; zero is a validation error (we have nothing to
// import). The mutual exclusivity is enforced at Validate time so the
// failure mode is "fix your YAML" rather than "watched the request go
// out and then 400'd."
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// skillAPIVersion / skillKind are the only literal envelope values
// this kind accepts. Declaring them as constants keeps the parse /
// dispatch loop in internal/manifest/parse.go from drifting away from
// this file when names get renamed.
const (
	skillAPIVersion = "crewship/v1"
	skillKind       = "Skill"
	skillKindName   = "skill" // lowercased label used in PlanItem.Kind
)

// maxSkillInlineBytes caps the inline body size. 8 KiB is a deliberate
// trade-off: large enough for a couple of pages of markdown (the
// median curated SKILL.md in internal/skills/bundled is ~3 KiB) but
// small enough that the YAML stays reviewable in a PR. Authors with
// larger bodies should use `path:`.
const maxSkillInlineBytes = 8 * 1024

// skillSlugPattern matches the slug shape the skills.ParseSKILLMD
// front-matter validator accepts (lower-snake or lower-kebab, leading
// alphanumeric, alphanumeric / underscore / hyphen thereafter). We
// re-validate here so a syntactically bad slug fails before the HTTP
// round-trip. The pattern is intentionally a SUPERSET of what the
// server enforces — false-positives at this layer would surface as
// 400s from import, which is acceptable; false-negatives let bad
// slugs slip past Validate which we want to avoid.
var skillSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ── Types ────────────────────────────────────────────────────────────────

// SkillSpec is the shape under `spec:` for kind: Skill.
//
// DisplayName, Category, Description, Icon are optional decoration;
// they end up in front-matter on the rendered SKILL.md if the body
// doesn't already carry them, so an operator who declares them at the
// manifest level doesn't need to duplicate them in the markdown
// itself. (The current bundle loader does NOT inject them — the
// importer reads front-matter from the body verbatim — so today these
// fields are forward-compatibility metadata. Wiring the merge belongs
// in a follow-up.)
//
// Inline / Path / Source are mutually exclusive. Validate enforces
// "exactly one"; zero is also an error since we'd have nothing to
// import.
type SkillSpec struct {
	// DisplayName is the human-facing label shown on the skill card.
	// When empty the server falls back to metadata.name. Forwarded
	// verbatim to import if it differs from the front-matter
	// display_name (we don't currently merge — see package note).
	DisplayName string `yaml:"display_name,omitempty" json:"display_name,omitempty"`

	// Category groups skills in the registry browser ("networking",
	// "research", …). Free-form string; the importer accepts unknown
	// values via the tolerant decoder.
	Category string `yaml:"category,omitempty" json:"category,omitempty"`

	// Description is a one-liner shown on the card. Required at the
	// manifest level even when the SKILL.md front-matter also carries
	// one — surfaces missing-description bugs at Validate rather than
	// at first-render.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Icon is a lucide-react icon slug. Optional decoration.
	Icon string `yaml:"icon,omitempty" json:"icon,omitempty"`

	// Inline is the SKILL.md body embedded directly in the manifest.
	// Capped at maxSkillInlineBytes; larger bodies should use `path:`.
	Inline string `yaml:"inline,omitempty" json:"inline,omitempty"`

	// Path is a manifest-relative path to a SKILL.md file. Resolved
	// by the bundle loader (parse.go:resolveLocalReferences) into the
	// unexported `resolved` field on SkillDocument. Validate does NOT
	// touch the filesystem.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`

	// Source is an HTTPS URL the import handler should fetch. The
	// fetch (and SSRF / license gates) live server-side; the manifest
	// layer only forwards the URL.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`

	// AllowUnsafeLicense bypasses the SPDX allowlist gate the
	// importer would otherwise apply. Per-skill flag; the manifest
	// CLI maps this to the existing /skills/import body field. Mirror
	// of the nested Skill struct in schema.go to keep round-trips
	// stable.
	AllowUnsafeLicense bool `yaml:"allow_unsafe_license,omitempty" json:"allow_unsafe_license,omitempty"`
}

// SkillDocument is the top-level YAML envelope for kind: Skill.
//
// `resolved` is the unexported cache for path-sourced bodies. The
// bundle loader populates it before Validate runs; Validate uses
// Resolved() to verify the body is present. Inline bodies are also
// mirrored into `resolved` by Validate so the rest of the pipeline can
// treat inline / path uniformly. URL sources leave `resolved` empty —
// the server fetches the body itself.
type SkillDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       SkillSpec            `yaml:"spec"       json:"spec"`

	// resolved holds the SKILL.md body after the bundle loader has
	// fetched it (for `path:` sources) or after Validate has
	// canonicalised it (for `inline:` sources). Not exported via YAML
	// — manifests authored by hand never set this.
	resolved string `yaml:"-" json:"-"`
}

// Resolved returns the SKILL.md body the import handler should send.
// Empty when the document uses `source:` (the server fetches itself)
// or when the bundle loader hasn't run yet (call sites should not
// rely on Resolved before Validate has been invoked at least once).
func (d *SkillDocument) Resolved() string { return d.resolved }

// SetResolved stores the SKILL.md body. Used by the parse-time bundle
// loader for `path:` sources and by tests. Manifest authors never call
// this directly.
func (d *SkillDocument) SetResolved(content string) { d.resolved = content }

// SkillRemote captures the bits of `GET /api/v1/skills` we care about
// for Plan. The full skillResponse shape in internal/api/skills.go
// includes lots of UI fluff (downloads, rating, avatars) that the
// manifest layer is not the source of truth for; we deliberately keep
// the local stub narrow so a server-side field rename doesn't break
// kind compilation.
//
// Source distinguishes BUNDLED (server-seeded; we refuse to plan
// changes against these) from CUSTOM / GENERATED rows the manifest
// owns.
type SkillRemote struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Source      string `json:"source"`
	Version     string `json:"version"`
	Vendor      string `json:"vendor"`
}

// ── Validate ─────────────────────────────────────────────────────────────

// Validate enforces structural rules without any HTTP or filesystem
// I/O. The check order is "cheap-and-local first" so the user sees the
// most specific error at the most local mistake.
//
// Rules:
//
//   - apiVersion (if set) must equal "crewship/v1"
//   - kind (if set) must equal "Skill"
//   - metadata.name required (human label on the registry card)
//   - metadata.slug required (idempotency key + cross-kind reference)
//   - metadata.slug matches skillSlugPattern
//   - spec.description required (UI invariant; surfacing at Validate
//     beats surprise-blank-card after Apply)
//   - exactly one of inline / path / source declared (xor)
//   - inline body length ≤ maxSkillInlineBytes
//   - source URL parses + uses https scheme + has a host
//   - if path was declared, the bundle loader must have populated
//     resolved (or the document was constructed by hand without going
//     through Load — Validate flags that case)
//
// The workspaceCtx is unused (Skill has no cross-kind FK references)
// but kept in the signature so the validate-phase dispatcher in
// internal/manifest/validate.go can invoke every kind through one
// interface.
//
// As a convenience side-effect, when `inline:` is set Validate copies
// the body into the `resolved` field so downstream code (Plan / Exec)
// can treat inline + path uniformly. Tests rely on this behaviour.
func (d *SkillDocument) Validate(_ internalapi.WorkspaceContext) error {
	if d.APIVersion != "" && d.APIVersion != skillAPIVersion {
		return fmt.Errorf("skill %q: apiVersion %q must be %q",
			d.Metadata.Slug, d.APIVersion, skillAPIVersion)
	}
	if d.Kind != "" && d.Kind != skillKind {
		return fmt.Errorf("skill %q: kind %q must be %q",
			d.Metadata.Slug, d.Kind, skillKind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("skill: metadata.name is required")
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("skill %q: metadata.slug is required", d.Metadata.Name)
	}
	if !skillSlugPattern.MatchString(d.Metadata.Slug) {
		return fmt.Errorf(
			"skill %q: metadata.slug %q must match pattern ^[a-z0-9][a-z0-9_-]*$ (lowercase alphanumeric, underscore, hyphen)",
			d.Metadata.Name, d.Metadata.Slug,
		)
	}

	// Description is required at the manifest level even when the
	// SKILL.md front-matter also carries one. The importer falls back
	// to NULL when both are empty, and an empty description renders
	// as a blank card — better to catch it here.
	if strings.TrimSpace(d.Spec.Description) == "" {
		return fmt.Errorf("skill %q: spec.description is required", d.Metadata.Slug)
	}

	// xor over the three source fields. Counting non-empty entries
	// keeps the message specific ("two sources declared: inline +
	// path") rather than a generic "exactly one of …".
	declared := make([]string, 0, 3)
	if d.Spec.Inline != "" {
		declared = append(declared, "inline")
	}
	if d.Spec.Path != "" {
		declared = append(declared, "path")
	}
	if d.Spec.Source != "" {
		declared = append(declared, "source")
	}
	switch len(declared) {
	case 0:
		return fmt.Errorf("skill %q: exactly one of spec.inline / spec.path / spec.source must be set (got none)", d.Metadata.Slug)
	case 1:
		// happy path
	default:
		return fmt.Errorf("skill %q: exactly one of spec.inline / spec.path / spec.source must be set (got %d: %s)",
			d.Metadata.Slug, len(declared), strings.Join(declared, " + "))
	}

	switch declared[0] {
	case "inline":
		if len(d.Spec.Inline) > maxSkillInlineBytes {
			return fmt.Errorf("skill %q: spec.inline body is %d bytes (max %d); move the SKILL.md to a sibling file and use spec.path",
				d.Metadata.Slug, len(d.Spec.Inline), maxSkillInlineBytes)
		}
		// Mirror inline → resolved so Plan can treat both source
		// shapes uniformly. Idempotent: re-validating after SetResolved
		// from a path source leaves resolved untouched (Inline is empty
		// in that case).
		d.resolved = d.Spec.Inline
	case "path":
		// Validate doesn't read the file itself — the bundle loader
		// does that during Load (see parse.go:resolveLocalReferences).
		// If the loader populated `resolved` we're good; otherwise the
		// document was hand-constructed without going through Load and
		// we have to flag that so Plan doesn't silently POST an empty
		// body. Tests that build SkillDocument literals must call
		// SetResolved before Validate.
		if d.resolved == "" {
			return fmt.Errorf("skill %q: spec.path %q is set but body was not resolved by the bundle loader (call SetResolved when constructing the document by hand)",
				d.Metadata.Slug, d.Spec.Path)
		}
	case "source":
		u, err := url.Parse(d.Spec.Source)
		if err != nil {
			return fmt.Errorf("skill %q: spec.source %q is not a valid URL: %v",
				d.Metadata.Slug, d.Spec.Source, err)
		}
		// HTTPS-only matches skills.ValidateImportURL — letting plain
		// http:// pass here would just surface as a 400 at apply time
		// (which is too late to do anything useful with). The check is
		// case-insensitive in the scheme to match url.Parse's
		// normalisation.
		if strings.ToLower(u.Scheme) != "https" {
			return fmt.Errorf("skill %q: spec.source must use https scheme (got %q)",
				d.Metadata.Slug, u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("skill %q: spec.source %q is missing a host",
				d.Metadata.Slug, d.Spec.Source)
		}
	}

	return nil
}

// ── Plan ─────────────────────────────────────────────────────────────────

// Plan compares the declared SkillDocument against the matched-by-slug
// remote row (nil if the skill doesn't exist yet) and emits exactly
// one PlanItem.
//
// Decision matrix:
//
//	remote == nil                → ActionCreate, POST /skills/import
//	remote != nil && drifted     → ActionUpdate, POST /skills/import (upsert)
//	remote != nil && unchanged   → ActionUnchanged (Exec=nil)
//	remote.Source == "BUNDLED"   → error (we refuse to touch curated rows;
//	                               mirrors the server-side guard in
//	                               importer.upsertEnriched)
//
// The Exec closure captures the document's body by value (not by
// pointer) so a per-document Plan called inside a range loop doesn't
// alias the wrong body when Apply later runs the closure. This is the
// same pattern the rest of the kinds package uses; see crew_template's
// Exec for the rationale comment.
func (d *SkillDocument) Plan(_ context.Context, c internalapi.Client, remote *SkillRemote) ([]internalapi.PlanItem, error) {
	wsID := c.WorkspaceID()
	if wsID == "" {
		return nil, fmt.Errorf("skill %q: workspace_id not set on client", d.Metadata.Slug)
	}

	// BUNDLED rows are server-managed. The import endpoint will refuse
	// to overwrite them (see internal/skills/importer.go:upsertEnriched)
	// so surfacing the error at Plan time gives the operator a clearer
	// dry-run report than a deferred 400.
	if remote != nil && strings.EqualFold(remote.Source, "BUNDLED") {
		return nil, fmt.Errorf("skill %q: refusing to plan changes against a BUNDLED skill (curated, ships with the binary). Pick a different slug or delete the bundled row first if intentional.",
			d.Metadata.Slug)
	}

	importPath := fmt.Sprintf("/api/v1/workspaces/%s/skills/import", wsID)
	body, err := d.buildImportBody()
	if err != nil {
		return nil, fmt.Errorf("skill %q: build import body: %w", d.Metadata.Slug, err)
	}

	if remote == nil {
		return []internalapi.PlanItem{{
			Kind:        skillKindName,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create skill %q from %s", d.Metadata.Slug, d.sourceLabel()),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return skillExec(ctx, c, "POST", importPath, body)
			},
		}}, nil
	}

	if !skillDocumentDiffers(d, remote) {
		return []internalapi.PlanItem{{
			Kind:        skillKindName,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("skill %q already matches", d.Metadata.Slug),
		}}, nil
	}

	return []internalapi.PlanItem{{
		Kind:        skillKindName,
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update skill %q from %s", d.Metadata.Slug, d.sourceLabel()),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			// The import endpoint is genuinely an upsert keyed on
			// slug, so the Update path uses the SAME POST as Create.
			// Differentiating Action lets the dry-run summary read
			// correctly while letting one server call cover both.
			return skillExec(ctx, c, "POST", importPath, body)
		},
	}}, nil
}

// sourceLabel returns the human-facing source descriptor used in plan
// item descriptions. Keeps the long string literals out of the Plan
// branches.
func (d *SkillDocument) sourceLabel() string {
	switch {
	case d.Spec.Inline != "":
		return "inline body"
	case d.Spec.Path != "":
		return "path " + d.Spec.Path
	case d.Spec.Source != "":
		return "url " + d.Spec.Source
	default:
		// Validate should have caught this; the fallback keeps the
		// description rendering safe rather than panicking.
		return "(no source)"
	}
}

// buildImportBody renders the POST body shape the import handler
// expects. The handler accepts either `url` or `content`, mutually
// exclusive (see internal/api/skills.go:Import). For inline + path we
// send `content` (the body the bundle loader / Validate produced); for
// source we send `url` and let the handler fetch it.
//
// `allow_unsafe_license` rides through verbatim so the importer can
// bypass the SPDX gate when the manifest explicitly opts in.
func (d *SkillDocument) buildImportBody() (map[string]any, error) {
	switch {
	case d.Spec.Source != "":
		return map[string]any{
			"url":                  d.Spec.Source,
			"allow_unsafe_license": d.Spec.AllowUnsafeLicense,
		}, nil
	case d.resolved != "":
		return map[string]any{
			"content":              d.resolved,
			"allow_unsafe_license": d.Spec.AllowUnsafeLicense,
		}, nil
	default:
		// Shouldn't happen post-Validate; surface a structured error
		// instead of silently posting an empty body.
		return nil, fmt.Errorf("no body to import (inline / path / source all empty)")
	}
}

// skillDocumentDiffers reports whether the declared document is
// equivalent to the server's row. Only fields the manifest is the
// source of truth for are compared:
//
//   - DisplayName / Description / Category / Icon — manifest-owned
//     decoration
//
// Body content (the SKILL.md markdown itself) is NOT compared field-by-
// field. The server doesn't expose the full content on the list
// endpoint and round-tripping the markdown through diff would be both
// expensive (extra round-trip per skill) and fragile (line-ending
// normalisation, front-matter ordering, …). Operators who want to
// force-push a new body should use the AllowUnsafeLicense flag or bump
// metadata.name; both are signal that "I'm changing this on purpose"
// and currently always produce an Update plan.
//
// As a result, "no metadata change, body change only" reports as
// Unchanged. This matches the pragmatic upsert semantics of the
// import handler — every Apply re-posts the body anyway, so a re-apply
// after editing the markdown will refresh the row server-side even if
// Plan called it Unchanged. The dry-run report just won't flag it. A
// future enhancement could hash the rendered body and store the hash
// in the description_quality column to enable content diffs.
func skillDocumentDiffers(d *SkillDocument, remote *SkillRemote) bool {
	desiredName := d.Spec.DisplayName
	if desiredName == "" {
		desiredName = d.Metadata.Name
	}
	if !equalNonEmpty(desiredName, remote.DisplayName) {
		return true
	}
	if !equalNonEmpty(d.Spec.Description, remote.Description) {
		return true
	}
	if !equalNonEmpty(d.Spec.Category, remote.Category) {
		return true
	}
	if !equalNonEmpty(d.Spec.Icon, remote.Icon) {
		return true
	}
	return false
}

// equalNonEmpty treats a blank declared value as "no opinion" — the
// manifest layer doesn't try to blank-out a server-set field unless
// the user explicitly sends a non-empty value that differs. Matches
// how Label / Project diff their optional fields.
func equalNonEmpty(declared, remote string) bool {
	if declared == "" {
		return true
	}
	return declared == remote
}

// ── Remote lookup ────────────────────────────────────────────────────────

// LookupSkillRemoteBySlug fetches `GET /api/v1/skills` and returns the
// row whose slug matches. Returns (nil, nil) when no row matches — the
// caller (typically Plan) treats nil as "skill doesn't exist yet".
//
// The list endpoint isn't paginated today (see internal/api/skills.go:List
// — it ORDERs everything and returns the lot) so a single GET is
// sufficient. If pagination lands later the slug-filter argument here
// can switch to a query-string filter on the server without changing
// any caller.
func LookupSkillRemoteBySlug(ctx context.Context, c internalapi.Client, slug string) (*SkillRemote, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("LookupSkillRemoteBySlug: slug is required")
	}
	rows, err := listSkills(ctx, c)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Slug == slug {
			row := rows[i]
			return &row, nil
		}
	}
	return nil, nil
}

// listSkills pulls /api/v1/skills and decodes the minimal shape this
// kind cares about. Tolerates both a flat array and a wrapped
// `{skills:[…]}` envelope so a future pagination wrapper doesn't break
// the kind silently.
func listSkills(ctx context.Context, c internalapi.Client) ([]SkillRemote, error) {
	resp, err := c.Get(ctx, "/api/v1/skills")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/skills: %w", err)
	}
	if err := checkStatus(resp, "list skills"); err != nil {
		return nil, err
	}
	data, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/skills body: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var flat []SkillRemote
	if err := json.Unmarshal(data, &flat); err == nil {
		return flat, nil
	}
	var wrapped struct {
		Skills []SkillRemote `json:"skills"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("decode /api/v1/skills: %w", err)
	}
	return wrapped.Skills, nil
}

// ── Export ───────────────────────────────────────────────────────────────

// ExportSkills produces one SkillDocument per non-BUNDLED row in the
// workspace's skill registry. BUNDLED rows are skipped: they're seeded
// by the server on every startup (see internal/skills/bundled) and
// re-applying them via manifest would either no-op or — if the
// operator edited the exported file — drift them in a way the next
// boot would revert. Treating BUNDLED as server-owned keeps the round
// trip idempotent.
//
// Caveat — body lossiness
// -----------------------
//
// The list endpoint does NOT return the SKILL.md content (the full
// body lives on the per-skill GET). Export therefore emits documents
// with metadata + decoration but WITHOUT a body source — neither
// inline, path, nor source is populated. The exported file is suitable
// for visual inspection and for cataloguing what the workspace has,
// but re-applying it as-is will fail Validate ("exactly one of inline
// / path / source must be set"). Operators who need a round-trip-safe
// export should pair this with `crewship skill pull` (a separate
// command not implemented here) to materialise SKILL.md files on disk
// and rewrite the docs with `path:` entries. When that command lands
// it can call back into a helper here; the export entry-point keeps
// the same signature.
//
// Output is stable-sorted by slug so diffs across runs are
// deterministic.
func ExportSkills(ctx context.Context, c internalapi.Client) ([]*SkillDocument, error) {
	rows, err := listSkills(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export skills: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]*SkillDocument, 0, len(rows))
	for _, row := range rows {
		if strings.EqualFold(row.Source, "BUNDLED") {
			continue
		}
		doc := &SkillDocument{
			APIVersion: skillAPIVersion,
			Kind:       skillKind,
			Metadata: internalapi.Metadata{
				Name:        displayOrName(row),
				Slug:        row.Slug,
				Description: row.Description,
			},
			Spec: SkillSpec{
				DisplayName: row.DisplayName,
				Category:    row.Category,
				Description: row.Description,
				Icon:        row.Icon,
				// Inline / Path / Source intentionally left empty —
				// see caveat in the doc comment.
			},
		}
		out = append(out, doc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Slug < out[j].Metadata.Slug })
	return out, nil
}

// displayOrName picks the best human-facing label for the exported
// metadata.name. Mirrors the importer's fallback so a round trip
// (import → export) doesn't accidentally rename a skill.
func displayOrName(row SkillRemote) string {
	if row.DisplayName != "" {
		return row.DisplayName
	}
	return row.Name
}

// ── REST helper ──────────────────────────────────────────────────────────

// skillExec runs a single mutating REST call and discards the body.
// Plan items don't need the response payload — apply.go tracks the
// result by counting Action types — so we just verify the status code
// and close the reader.
//
// File-local helper (prefixed name) to avoid collisions with the
// `execAndDiscard` / `workflowExec` other kind files declare in the
// same package; keeping the helper local keeps this file
// self-contained and resilient to neighbouring agents shuffling their
// utilities around.
func skillExec(ctx context.Context, c internalapi.Client, method, path string, body any) error {
	var (
		resp *internalapi.Response
		err  error
	)
	switch method {
	case "POST":
		resp, err = c.Post(ctx, path, body)
	case "PATCH":
		resp, err = c.Patch(ctx, path, body)
	case "PUT":
		resp, err = c.Put(ctx, path, body)
	case "DELETE":
		resp, err = c.Delete(ctx, path)
	default:
		return fmt.Errorf("unsupported HTTP method %q", method)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp == nil {
		return fmt.Errorf("%s %s: nil response", method, path)
	}
	// Drain & close so the underlying connection can be reused. The
	// 4 KiB cap is enough to surface a meaningful error message
	// without slurping a stray binary blob.
	var bodyBytes []byte
	if resp.Body != nil {
		bodyBytes, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, bodyBytes)
	}
	return nil
}
