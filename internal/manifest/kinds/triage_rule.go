// Package kinds holds per-kind manifest implementations. Each file
// here is the self-contained contract for one `kind:` value — its
// document shape, validation rules, planning logic, and round-trip
// export. The parent `internal/manifest` package wires these into
// the parse/validate/apply/export pipeline; kinds never import the
// parent (avoiding an import cycle), they depend only on the
// `internalapi` leaf package for shared interfaces and value types.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// TriageRuleSpec is the shape under `spec:` for kind: TriageRule.
//
// The on-disk YAML carries structured nested `match` / `actions`
// objects so the user authors readable rules. The server stores
// these as opaque JSON TEXT columns (match_json, actions_json), so
// Plan marshals them at apply-time and ExportTriageRules unmarshals
// them on the way out.
type TriageRuleSpec struct {
	// Enabled toggles the rule without removing it. Default: true.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Priority is the rule evaluation order (lower runs first).
	// Default: 100 if zero/unset.
	Priority int `yaml:"priority" json:"priority"`

	// Match defines the conditions an incoming issue must satisfy
	// (all fields AND-ed together) for the rule to fire.
	Match TriageMatch `yaml:"match" json:"match"`

	// Actions defines the mutations applied when Match passes.
	Actions TriageActions `yaml:"actions" json:"actions"`
}

// TriageMatch is the structured match condition. At least one field
// must be non-empty for the rule to be considered well-formed.
type TriageMatch struct {
	TitleContains []string `yaml:"title_contains,omitempty" json:"title_contains,omitempty"`
	BodyContains  []string `yaml:"body_contains,omitempty"  json:"body_contains,omitempty"`
	FromAgentSlug string   `yaml:"from_agent_slug,omitempty" json:"from_agent_slug,omitempty"`
	FromCrewSlug  string   `yaml:"from_crew_slug,omitempty"  json:"from_crew_slug,omitempty"`
}

// IsEmpty reports whether the match has no conditions at all.
// A rule with an empty match would fire on every issue, which is
// almost certainly a manifest authoring mistake — Validate rejects
// it so the user has to spell out their intent.
func (m TriageMatch) IsEmpty() bool {
	return len(m.TitleContains) == 0 &&
		len(m.BodyContains) == 0 &&
		strings.TrimSpace(m.FromAgentSlug) == "" &&
		strings.TrimSpace(m.FromCrewSlug) == ""
}

// TriageActions is the structured action block. All fields are
// optional individually; a rule with zero actions is allowed (it
// can still be useful as a "matched" counter), but typically at
// least one is set.
type TriageActions struct {
	AddLabels           []string `yaml:"add_labels,omitempty"            json:"add_labels,omitempty"`
	SetPriority         string   `yaml:"set_priority,omitempty"          json:"set_priority,omitempty"`
	AssignToProjectSlug string   `yaml:"assign_to_project_slug,omitempty" json:"assign_to_project_slug,omitempty"`
	AssignToAgentSlug   string   `yaml:"assign_to_agent_slug,omitempty"  json:"assign_to_agent_slug,omitempty"`
	SetStatus           string   `yaml:"set_status,omitempty"            json:"set_status,omitempty"`
}

// TriageRuleDocument is the top-level document shape — apiVersion +
// kind + metadata + spec. Decoded directly from a YAML node by the
// parent manifest parser's kind switch.
type TriageRuleDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       TriageRuleSpec       `yaml:"spec"       json:"spec"`
}

// TriageRuleRemote is the server-side row shape for diffing. The
// server stores match/actions as JSON-encoded TEXT columns;
// matching the manifest spec requires unmarshaling them.
type TriageRuleRemote struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	MatchJSON   string `json:"match_json"`
	ActionsJSON string `json:"actions_json"`
}

// ── Validate ───────────────────────────────────────────────────────

// Validate enforces structural rules:
//   - Match must have at least one non-empty condition
//   - Every label slug in Actions.AddLabels must be declared/remote
//   - Actions.AssignToProjectSlug, if set, must resolve
//   - Actions.AssignToAgentSlug, if set, must resolve
//   - Match.FromAgentSlug / FromCrewSlug, if set, must resolve
//
// Returns a single error joining all violations (rather than
// stopping at the first) so the user sees every problem in one
// pass — typical for declarative tools where iterating on
// validation feedback is painful.
func (d *TriageRuleDocument) Validate(ctx internalapi.WorkspaceContext) error {
	var problems []string

	if strings.TrimSpace(d.Metadata.Slug) == "" {
		problems = append(problems, "metadata.slug is required")
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		problems = append(problems, "metadata.name is required")
	}

	if d.Spec.Match.IsEmpty() {
		problems = append(problems,
			"spec.match must have at least one non-empty condition (title_contains, body_contains, from_agent_slug, or from_crew_slug)")
	}

	for _, lbl := range d.Spec.Actions.AddLabels {
		lbl = strings.TrimSpace(lbl)
		if lbl == "" {
			problems = append(problems, "spec.actions.add_labels contains an empty slug")
			continue
		}
		if !ctx.HasLabel(lbl) {
			problems = append(problems,
				fmt.Sprintf("spec.actions.add_labels references unknown label slug %q", lbl))
		}
	}

	if proj := strings.TrimSpace(d.Spec.Actions.AssignToProjectSlug); proj != "" {
		if !ctx.HasProject(proj) {
			problems = append(problems,
				fmt.Sprintf("spec.actions.assign_to_project_slug references unknown project slug %q", proj))
		}
	}

	if agent := strings.TrimSpace(d.Spec.Actions.AssignToAgentSlug); agent != "" {
		if !ctx.HasAgent(agent) {
			problems = append(problems,
				fmt.Sprintf("spec.actions.assign_to_agent_slug references unknown agent slug %q", agent))
		}
	}

	if agent := strings.TrimSpace(d.Spec.Match.FromAgentSlug); agent != "" {
		if !ctx.HasAgent(agent) {
			problems = append(problems,
				fmt.Sprintf("spec.match.from_agent_slug references unknown agent slug %q", agent))
		}
	}

	if crew := strings.TrimSpace(d.Spec.Match.FromCrewSlug); crew != "" {
		if !ctx.HasCrew(crew) {
			problems = append(problems,
				fmt.Sprintf("spec.match.from_crew_slug references unknown crew slug %q", crew))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("TriageRule %q invalid:\n  - %s",
			d.Metadata.Slug, strings.Join(problems, "\n  - "))
	}
	return nil
}

// ── Plan ───────────────────────────────────────────────────────────

const triageRulesPath = "/api/v1/triage-rules"

// effectivePriority returns the priority that should be sent over the
// wire — zero is treated as "use the default" per the spec (100).
func (d *TriageRuleDocument) effectivePriority() int {
	if d.Spec.Priority == 0 {
		return 100
	}
	return d.Spec.Priority
}

// buildPostBody marshals the structured match/actions blocks into
// the server's flat JSON-TEXT shape:
//
//	{name, enabled, priority, match_json, actions_json}
//
// Marshaling errors here would indicate a bug in our types, since
// every TriageMatch/TriageActions field is JSON-marshalable — but
// we still return the error rather than panicking so the caller
// can include the rule slug in any user-facing message.
func (d *TriageRuleDocument) buildPostBody() (map[string]any, error) {
	matchBytes, err := json.Marshal(d.Spec.Match)
	if err != nil {
		return nil, fmt.Errorf("marshal match: %w", err)
	}
	actionsBytes, err := json.Marshal(d.Spec.Actions)
	if err != nil {
		return nil, fmt.Errorf("marshal actions: %w", err)
	}
	return map[string]any{
		"name":         d.Metadata.Name,
		"slug":         d.Metadata.Slug,
		"enabled":      d.Spec.Enabled,
		"priority":     d.effectivePriority(),
		"match_json":   string(matchBytes),
		"actions_json": string(actionsBytes),
	}, nil
}

// equalsRemote reports whether the declared document matches the
// remote row byte-for-byte after re-marshaling our local structs.
// Comparing the freshly-marshaled JSON (rather than the YAML or the
// raw remote JSON string) is the only stable approach: the server
// could reformat key order or whitespace, so we unmarshal the
// remote JSON into the same struct type and marshal both sides with
// the same encoder before comparing.
func (d *TriageRuleDocument) equalsRemote(remote *TriageRuleRemote) (bool, error) {
	if remote == nil {
		return false, nil
	}
	if remote.Name != d.Metadata.Name {
		return false, nil
	}
	if remote.Enabled != d.Spec.Enabled {
		return false, nil
	}
	if remote.Priority != d.effectivePriority() {
		return false, nil
	}

	var remoteMatch TriageMatch
	if strings.TrimSpace(remote.MatchJSON) != "" {
		if err := json.Unmarshal([]byte(remote.MatchJSON), &remoteMatch); err != nil {
			// Server returned a corrupt match_json — treat as drift
			// so we re-write it on the next apply. This is the
			// least-surprising recovery path; failing hard would
			// block applies until an operator hand-fixes the DB.
			return false, nil
		}
	}

	var remoteActions TriageActions
	if strings.TrimSpace(remote.ActionsJSON) != "" {
		if err := json.Unmarshal([]byte(remote.ActionsJSON), &remoteActions); err != nil {
			return false, nil
		}
	}

	declaredMatchBytes, err := json.Marshal(d.Spec.Match)
	if err != nil {
		return false, err
	}
	remoteMatchBytes, err := json.Marshal(remoteMatch)
	if err != nil {
		return false, err
	}
	if string(declaredMatchBytes) != string(remoteMatchBytes) {
		return false, nil
	}

	declaredActionsBytes, err := json.Marshal(d.Spec.Actions)
	if err != nil {
		return false, err
	}
	remoteActionsBytes, err := json.Marshal(remoteActions)
	if err != nil {
		return false, err
	}
	if string(declaredActionsBytes) != string(remoteActionsBytes) {
		return false, nil
	}

	return true, nil
}

// Plan returns the work needed to make the remote match the
// declared document.
//
//   - remote == nil → Action=Create  (POST /api/v1/triage-rules)
//   - remote present & equal → Action=Unchanged
//   - remote present & drifted → Action=Update (PATCH /api/v1/triage-rules/{id})
//
// Delete-then-recreate for ApplyReplace mode is the parent
// apply.go's responsibility; this method only emits create/update/
// unchanged. The remote is fetched by the parent layer via
// ExportTriageRules-style listing + slug match before calling Plan.
func (d *TriageRuleDocument) Plan(
	_ context.Context,
	_ internalapi.Client,
	remote *TriageRuleRemote,
) ([]internalapi.PlanItem, error) {
	body, err := d.buildPostBody()
	if err != nil {
		return nil, fmt.Errorf("plan triage rule %q: %w", d.Metadata.Slug, err)
	}

	if remote == nil {
		item := internalapi.PlanItem{
			Kind:   "triage_rule",
			Slug:   d.Metadata.Slug,
			Action: internalapi.ActionCreate,
			Description: fmt.Sprintf("Create triage rule %q (priority=%d, enabled=%t)",
				d.Metadata.Name, d.effectivePriority(), d.Spec.Enabled),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Post(ctx, triageRulesPath, body)
				if err != nil {
					return fmt.Errorf("create triage rule %q: %w", d.Metadata.Slug, err)
				}
				return triageRuleDrainAndCheck(resp, http.StatusCreated, http.StatusOK)
			},
		}
		return []internalapi.PlanItem{item}, nil
	}

	equal, err := d.equalsRemote(remote)
	if err != nil {
		return nil, fmt.Errorf("diff triage rule %q: %w", d.Metadata.Slug, err)
	}
	if equal {
		return []internalapi.PlanItem{{
			Kind:        "triage_rule",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("Triage rule %q already in desired state", d.Metadata.Name),
		}}, nil
	}

	remoteID := remote.ID
	item := internalapi.PlanItem{
		Kind:   "triage_rule",
		Slug:   d.Metadata.Slug,
		Action: internalapi.ActionUpdate,
		Description: fmt.Sprintf("Update triage rule %q (priority=%d, enabled=%t)",
			d.Metadata.Name, d.effectivePriority(), d.Spec.Enabled),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			resp, err := c.Patch(ctx, triageRulesPath+"/"+remoteID, body)
			if err != nil {
				return fmt.Errorf("update triage rule %q: %w", d.Metadata.Slug, err)
			}
			return triageRuleDrainAndCheck(resp, http.StatusOK)
		},
	}
	return []internalapi.PlanItem{item}, nil
}

// ── Export ─────────────────────────────────────────────────────────

// ExportTriageRules pulls every triage rule from the server and
// rebuilds TriageRuleDocument values for round-trip — used by
// `crewship export`. The reverse of buildPostBody: unmarshal the
// match_json / actions_json TEXT columns into structured spec
// fields so the on-disk YAML stays human-readable rather than
// echoing JSON blobs back to the user.
//
// The function tolerates malformed match_json/actions_json on the
// server (logs nothing, leaves the corresponding spec field at its
// zero value) because export should never fail outright on data
// that's already in the database — the user can fix the export
// output by hand if needed.
func ExportTriageRules(ctx context.Context, c internalapi.Client) ([]*TriageRuleDocument, error) {
	resp, err := c.Get(ctx, triageRulesPath)
	if err != nil {
		return nil, fmt.Errorf("list triage rules: %w", err)
	}
	body, err := triageRuleReadAllAndCheck(resp, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("list triage rules: %w", err)
	}

	var remotes []TriageRuleRemote
	if len(body) > 0 {
		if err := json.Unmarshal(body, &remotes); err != nil {
			return nil, fmt.Errorf("decode triage rules list: %w", err)
		}
	}

	out := make([]*TriageRuleDocument, 0, len(remotes))
	for _, r := range remotes {
		doc := &TriageRuleDocument{
			APIVersion: "crewship/v1",
			Kind:       "TriageRule",
			Metadata: internalapi.Metadata{
				Name: r.Name,
				// Server doesn't store a separate slug column for
				// triage rules; the manifest uses metadata.slug as
				// the idempotency key and we deterministically
				// derive it from the name. Operators can override
				// by editing the exported YAML before re-apply.
				Slug: triageRuleSlugifyName(r.Name),
			},
			Spec: TriageRuleSpec{
				Enabled:  r.Enabled,
				Priority: r.Priority,
			},
		}

		if strings.TrimSpace(r.MatchJSON) != "" {
			var m TriageMatch
			if err := json.Unmarshal([]byte(r.MatchJSON), &m); err == nil {
				doc.Spec.Match = m
			}
		}
		if strings.TrimSpace(r.ActionsJSON) != "" {
			var a TriageActions
			if err := json.Unmarshal([]byte(r.ActionsJSON), &a); err == nil {
				doc.Spec.Actions = a
			}
		}

		out = append(out, doc)
	}
	return out, nil
}

// ── small helpers (file-local; not exported) ────────────────────────
//
// Helper names are prefixed `triageRule` so they don't collide with
// similar utilities other kinds' `*.go` files in this same package
// have declared (e.g. instance_setting.go also defines a firstBytes
// for error truncation).

// triageRuleDrainAndCheck closes the response body and returns an
// error if the status code isn't one of the accepted ones. Used
// after mutating calls where we don't need to decode the response
// payload.
func triageRuleDrainAndCheck(resp *internalapi.Response, accepted ...int) error {
	if resp == nil {
		return fmt.Errorf("nil response")
	}
	if rc, ok := resp.Body.(io.Closer); ok {
		defer rc.Close()
	}
	// Drain the body so the underlying connection can be reused —
	// http.Response semantics require the body be read to EOF even
	// when the caller doesn't care about the payload.
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	for _, ok := range accepted {
		if resp.StatusCode == ok {
			return nil
		}
	}
	return fmt.Errorf("unexpected status %d", resp.StatusCode)
}

// triageRuleReadAllAndCheck reads the full response body, closes
// it, and verifies the status code. Returns the body bytes for the
// caller to JSON-decode.
func triageRuleReadAllAndCheck(resp *internalapi.Response, accepted ...int) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("nil response")
	}
	if rc, ok := resp.Body.(io.Closer); ok {
		defer rc.Close()
	}
	var body []byte
	if resp.Body != nil {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		body = b
	}
	for _, ok := range accepted {
		if resp.StatusCode == ok {
			return body, nil
		}
	}
	return body, fmt.Errorf("unexpected status %d: %s",
		resp.StatusCode, triageRuleFirstBytes(body, 200))
}

// triageRuleSlugifyName produces a deterministic kebab-case slug
// from a free-form name. Strips non-[a-z0-9-] characters, collapses
// runs of hyphens, and trims leading/trailing hyphens. Used only by
// Export since the server doesn't store a slug column for triage
// rules.
func triageRuleSlugifyName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	b.Grow(len(name))
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "triage-rule"
	}
	return out
}

// triageRuleFirstBytes returns at most n bytes of b as a printable
// string, used for error messages so we don't dump megabytes of
// HTML into the user's terminal when the server returns an error
// page.
func triageRuleFirstBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
