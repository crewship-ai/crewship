// Package kinds holds one Go file per declarative manifest kind. The
// per-kind types + Validate / Plan / Export functions live here so the
// top-level manifest package can dispatch on Kind without importing
// dozens of internal sub-packages. Shared interfaces (Client,
// PlanItem, WorkspaceContext) come from internal/manifest/internalapi
// to break the import cycle between manifest and kinds.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"github.com/robfig/cron/v3"
)

// ---------- type surface ----------

// RoutineSpec is the shape under `spec:` for kind: Routine. The first
// block of fields is the existing routine.v1.json DSL embedded verbatim
// — same names, same semantics — so a `crewship routine save -f
// routine.json` file body can be lifted into the manifest with no field
// renames. The trailing schedules + webhook fields are manifest-only
// additions that map to the sibling pipeline_schedules / pipeline_webhooks
// tables. The compound shape lets one routine manifest deploy a
// pipeline + its triggers in one apply, which the JSON-only flow could
// not express without a separate `crewship schedule create` call.
type RoutineSpec struct {
	DSLVersion               string        `yaml:"dsl_version"                          json:"dsl_version"`
	Description              string        `yaml:"description,omitempty"                json:"description,omitempty"`
	Inputs                   []any         `yaml:"inputs,omitempty"                     json:"inputs,omitempty"`
	Steps                    []RoutineStep `yaml:"steps"                                json:"steps"`
	CredentialsRequired      []any         `yaml:"credentials_required,omitempty"       json:"credentials_required,omitempty"`
	EstimatedCostUSD         float64       `yaml:"estimated_cost_usd,omitempty"         json:"estimated_cost_usd,omitempty"`
	EstimatedDurationSeconds int           `yaml:"estimated_duration_seconds,omitempty" json:"estimated_duration_seconds,omitempty"`
	MaxCostUSD               float64       `yaml:"max_cost_usd,omitempty"               json:"max_cost_usd,omitempty"`
	EgressTargets            []string      `yaml:"egress_targets,omitempty"             json:"egress_targets,omitempty"`

	// Schedules are nested cron triggers; each entry will become one
	// pipeline_schedules row when applied. Zero schedules = no triggers
	// (the routine can still be invoked manually via /run).
	Schedules []RoutineSchedule `yaml:"schedules,omitempty" json:"schedules,omitempty"`

	// Webhook is the optional public-dispatch endpoint. At most one
	// webhook per routine (the pipeline_webhooks store enforces this
	// at apply time; the manifest mirrors the cardinality).
	Webhook *RoutineWebhook `yaml:"webhook,omitempty" json:"webhook,omitempty"`
}

// RoutineStep is a thin pass-through of the routine.v1.json Step
// shape. We intentionally don't model every per-type sub-struct here:
// the server's pipeline.Parse already validates the DSL exhaustively,
// and the manifest layer only needs to extract `agent_slug` for FK
// validation (see Validate). All other fields ride through as raw YAML
// nodes via the catch-all map below.
type RoutineStep struct {
	ID        string `yaml:"id"                  json:"id"`
	Type      string `yaml:"type"                json:"type"`
	AgentSlug string `yaml:"agent_slug,omitempty" json:"agent_slug,omitempty"`

	// Rest captures every other field on the step so a round-trip
	// through Marshal preserves the original DSL. We use an
	// inline-style catch-all map; the server's Parse handles the
	// remaining keys when the routine is created.
	Rest map[string]any `yaml:",inline" json:"-"`
}

// MarshalJSON merges Rest with the typed fields so the wire body sent
// to /pipelines/save reproduces the original step shape. JSON marshal
// of a struct with both typed + extra fields requires hand-rolled
// merging because encoding/json doesn't support yaml's `,inline` tag.
func (s RoutineStep) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for k, v := range s.Rest {
		out[k] = v
	}
	out["id"] = s.ID
	out["type"] = s.Type
	if s.AgentSlug != "" {
		out["agent_slug"] = s.AgentSlug
	}
	return json.Marshal(out)
}

// UnmarshalJSON is the inverse: pull `id` / `type` / `agent_slug` into
// the typed fields and stash the rest in Rest. Lets manifest authors
// write JSON or YAML interchangeably.
func (s *RoutineStep) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["id"].(string); ok {
		s.ID = v
	}
	if v, ok := raw["type"].(string); ok {
		s.Type = v
	}
	if v, ok := raw["agent_slug"].(string); ok {
		s.AgentSlug = v
	}
	delete(raw, "id")
	delete(raw, "type")
	delete(raw, "agent_slug")
	s.Rest = raw
	return nil
}

// RoutineSchedule is the per-cron sub-document. Maps 1:1 to a row in
// pipeline_schedules; `name` is the human label that shows up in the
// schedules list. Inputs override the routine defaults at trigger time
// — a routine with `channels=all` default can have one schedule that
// passes `channels=eng-only` so the cron run is scoped differently
// from manual invocations.
type RoutineSchedule struct {
	Name     string         `yaml:"name"               json:"name"`
	Cron     string         `yaml:"cron"               json:"cron"`
	Timezone string         `yaml:"timezone"           json:"timezone"`
	Enabled  *bool          `yaml:"enabled,omitempty"  json:"enabled,omitempty"`
	Inputs   map[string]any `yaml:"inputs,omitempty"   json:"inputs,omitempty"`
}

// EnabledOrDefault returns the schedule's enabled flag, defaulting to
// true when the manifest omitted the field. Pointer-bool lets us tell
// "explicitly false" from "omitted".
func (s *RoutineSchedule) EnabledOrDefault() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// RoutineWebhook is the optional sibling-doc that maps to a single
// pipeline_webhooks row. Token-based auth is the default; setting
// require_token=false makes the webhook publicly invokable (typically
// only meaningful behind a private network gateway).
type RoutineWebhook struct {
	Enabled      bool   `yaml:"enabled"                  json:"enabled"`
	RequireToken *bool  `yaml:"require_token,omitempty"  json:"require_token,omitempty"`
	TokenEnvRef  string `yaml:"token_env_ref,omitempty"  json:"token_env_ref,omitempty"`
}

// RequireTokenOrDefault honors `require_token: true` as the default.
// Authors must explicitly set it to false to opt into open dispatch.
func (w *RoutineWebhook) RequireTokenOrDefault() bool {
	if w.RequireToken == nil {
		return true
	}
	return *w.RequireToken
}

// RoutineDocument is the top-level YAML shape for kind: Routine. The
// crew this routine belongs to is carried in metadata.labels.crew —
// every routine MUST declare a parent crew because agent_slugs are
// resolved within that crew at run time.
type RoutineDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       RoutineSpec          `yaml:"spec"       json:"spec"`
}

// RoutineRemote is what we read back from GET /pipelines/{slug} to
// decide whether the routine has drifted. Only the fields the manifest
// is the source of truth for are kept; ephemeral runtime fields like
// invocation_count / last_invoked_at are ignored on diff.
type RoutineRemote struct {
	ID             string          `json:"id"`
	Slug           string          `json:"slug"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	DefinitionJSON json.RawMessage `json:"definition"`
	AuthorCrewID   string          `json:"author_crew_id"`
}

// ScheduleRemote mirrors a single pipeline_schedules row from
// /pipeline-schedules. We compare on (name, cron, timezone, enabled,
// inputs) — same set the routine manifest authoritatively owns.
type ScheduleRemote struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	TargetPipelineID   string         `json:"target_pipeline_id"`
	TargetPipelineSlug string         `json:"target_pipeline_slug"`
	CronExpr           string         `json:"cron_expr"`
	Timezone           string         `json:"timezone"`
	Inputs             map[string]any `json:"inputs"`
	Enabled            bool           `json:"enabled"`
}

// WebhookRemote mirrors a pipeline_webhooks row. Token + signing
// secret are excluded from diff — they're server-minted and not
// manifest-controlled.
type WebhookRemote struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	TargetPipelineID   string `json:"target_pipeline_id"`
	TargetPipelineSlug string `json:"target_pipeline_slug"`
	Enabled            bool   `json:"enabled"`
	SigningSecretSet   bool   `json:"signing_secret_set"`
}

// ---------- validation ----------

// Validate enforces the structural rules a routine document must obey
// before Plan can build a coherent set of REST calls. The checks are
// deliberately conservative: anything we can't verify client-side
// (e.g. that the crew actually contains the named agent) becomes a
// server-side error at apply time, surfaced through the normal REST
// error path. Validate's job is to catch the typos that would
// otherwise cost an apply round-trip.
//
// Returns nil on success; returns a single error joining every rule
// that failed so the user fixes them all at once instead of one apply
// per typo.
func (d *RoutineDocument) Validate(ctx internalapi.WorkspaceContext) error {
	var errs []string

	if strings.TrimSpace(d.Metadata.Slug) == "" {
		errs = append(errs, "metadata.slug required")
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		errs = append(errs, "metadata.name required")
	}

	// Crew label is the parent-FK reference; required because agent
	// slugs resolve inside that crew. We accept either a declared crew
	// (same manifest) or a remote crew (already on the server).
	crewSlug := strings.TrimSpace(d.Metadata.Labels["crew"])
	if crewSlug == "" {
		errs = append(errs, "metadata.labels.crew required (routines are crew-scoped)")
	} else if !ctx.HasCrew(crewSlug) {
		errs = append(errs, fmt.Sprintf("metadata.labels.crew %q not found in workspace", crewSlug))
	}

	if d.Spec.DSLVersion == "" {
		errs = append(errs, "spec.dsl_version required")
	}
	if len(d.Spec.Steps) == 0 {
		errs = append(errs, "spec.steps must have at least one step")
	}

	// Cross-check every agent_run step's agent_slug against the
	// workspace's known agents. We deliberately do NOT scope this to
	// "agents in the parent crew" — that cross-check needs crew-agent
	// join data we don't have in WorkspaceContext today. The server
	// re-validates at apply time and will reject the save if the
	// agent isn't a member of the parent crew, so the manifest layer
	// can stay strictly slug-aware.
	for i, step := range d.Spec.Steps {
		if step.Type == "agent_run" && step.AgentSlug != "" {
			if !ctx.HasAgent(step.AgentSlug) {
				errs = append(errs, fmt.Sprintf("spec.steps[%d].agent_slug %q not found in workspace", i, step.AgentSlug))
			}
		}
	}

	// Cron + timezone validation per schedule. We use the same parser
	// flags as the pipeline.ScheduleStore so a manifest that validates
	// here is guaranteed to apply cleanly downstream.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	seenScheduleNames := map[string]int{}
	for i, sched := range d.Spec.Schedules {
		if strings.TrimSpace(sched.Name) == "" {
			errs = append(errs, fmt.Sprintf("spec.schedules[%d].name required", i))
		} else {
			if prev, dup := seenScheduleNames[sched.Name]; dup {
				errs = append(errs, fmt.Sprintf("spec.schedules[%d].name duplicates schedules[%d]", i, prev))
			}
			seenScheduleNames[sched.Name] = i
		}
		if sched.Cron == "" {
			errs = append(errs, fmt.Sprintf("spec.schedules[%d].cron required", i))
		} else if _, err := parser.Parse(sched.Cron); err != nil {
			errs = append(errs, fmt.Sprintf("spec.schedules[%d].cron invalid: %v", i, err))
		}
		if sched.Timezone == "" {
			errs = append(errs, fmt.Sprintf("spec.schedules[%d].timezone required (IANA name e.g. UTC, Europe/Prague)", i))
		} else if _, err := time.LoadLocation(sched.Timezone); err != nil {
			errs = append(errs, fmt.Sprintf("spec.schedules[%d].timezone invalid: %v", i, err))
		}
	}

	// Webhook intentionally has no Validate-time checks beyond the
	// type being well-formed. token_env_ref resolution is deferred to
	// the Plan layer (surfaced as an advisory line on the report)
	// rather than blocking validate. That way an Export → re-apply
	// round trip — which loses token_env_ref because the server
	// doesn't store it — still passes Validate cleanly; the operator
	// fixes the env-ref the next time they edit the file.
	_ = d.Spec.Webhook

	if len(errs) > 0 {
		return fmt.Errorf("routine %q: %s", d.Metadata.Slug, strings.Join(errs, "; "))
	}
	return nil
}

// ---------- plan ----------

// Plan compares the declared routine document (plus its nested
// schedules + webhook) against remote state and emits one PlanItem per
// distinct REST mutation. The compound shape means a single
// RoutineDocument can produce up to 2 + len(schedules)*2 + 2 plan
// items (routine create/update + per-schedule create/update + webhook
// create/update + delete-the-drifted items). Apply executes them in
// the returned order; for routines that order matters because
// schedules and the webhook FK back to the pipeline that must already
// exist.
//
// `remote` is the matched-by-slug pipeline (or nil if the routine is
// new). Plan fetches schedules + webhook for that pipeline itself —
// the outer manifest layer doesn't carry the per-routine sub-fetches.
func (d *RoutineDocument) Plan(ctx context.Context, c internalapi.Client, remote *RoutineRemote) ([]internalapi.PlanItem, error) {
	wsID := c.WorkspaceID()
	if wsID == "" {
		return nil, fmt.Errorf("routine %q: workspace_id not set on client", d.Metadata.Slug)
	}
	var items []internalapi.PlanItem

	// --- 1. routine row itself ---
	routineBody := d.buildSaveBody()
	if remote == nil {
		items = append(items, internalapi.PlanItem{
			Kind:        "routine",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create routine %s (crew=%s, %d steps)", d.Metadata.Slug, d.Metadata.Labels["crew"], len(d.Spec.Steps)),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return saveRoutine(ctx, c, wsID, routineBody)
			},
		})
	} else if routineDiffers(d, remote) {
		items = append(items, internalapi.PlanItem{
			Kind:        "routine",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUpdate,
			Description: fmt.Sprintf("update routine %s", d.Metadata.Slug),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return saveRoutine(ctx, c, wsID, routineBody)
			},
		})
	} else {
		items = append(items, internalapi.PlanItem{
			Kind:        "routine",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("routine %s unchanged", d.Metadata.Slug),
		})
	}

	// --- 2. schedules ---
	// Fetch what's currently there and diff against the declared set.
	// pipeline-schedules has no `?pipeline_slug=` filter today, so we
	// list the whole workspace and filter client-side; the slug index
	// makes this cheap until a workspace grows past hundreds of
	// schedules.
	remoteScheds, err := listRoutineSchedules(ctx, c, wsID, d.Metadata.Slug)
	if err != nil {
		return nil, fmt.Errorf("routine %q: list schedules: %w", d.Metadata.Slug, err)
	}
	remoteByName := map[string]*ScheduleRemote{}
	for i := range remoteScheds {
		remoteByName[remoteScheds[i].Name] = &remoteScheds[i]
	}
	declaredByName := map[string]int{}
	for i, sc := range d.Spec.Schedules {
		declaredByName[sc.Name] = i
	}

	for i, sc := range d.Spec.Schedules {
		sched := sc // capture for closure
		idx := i
		body := buildScheduleBody(d.Metadata.Slug, &sched)
		existing := remoteByName[sched.Name]
		switch {
		case existing == nil:
			items = append(items, internalapi.PlanItem{
				Kind:        "schedule",
				Slug:        d.Metadata.Slug + "." + sched.Name,
				Action:      internalapi.ActionCreate,
				Description: fmt.Sprintf("create schedule %s.%s (cron=%s tz=%s)", d.Metadata.Slug, sched.Name, sched.Cron, sched.Timezone),
				Exec: func(ctx context.Context, c internalapi.Client) error {
					_, err := jsonPost(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", wsID), body)
					return err
				},
			})
		case scheduleDiffers(&sched, existing):
			schedID := existing.ID
			items = append(items, internalapi.PlanItem{
				Kind:        "schedule",
				Slug:        d.Metadata.Slug + "." + sched.Name,
				Action:      internalapi.ActionUpdate,
				Description: fmt.Sprintf("update schedule %s.%s", d.Metadata.Slug, sched.Name),
				Exec: func(ctx context.Context, c internalapi.Client) error {
					_, err := jsonPatch(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules/%s", wsID, schedID), body)
					return err
				},
			})
		default:
			items = append(items, internalapi.PlanItem{
				Kind:        "schedule",
				Slug:        d.Metadata.Slug + "." + sched.Name,
				Action:      internalapi.ActionUnchanged,
				Description: fmt.Sprintf("schedule %s.%s unchanged", d.Metadata.Slug, sched.Name),
			})
		}
		_ = idx
	}

	// Drop any remote schedule that the manifest no longer declares.
	// Sorted iteration so the plan output is stable across runs (map
	// iteration order would jitter the dry-run diff).
	staleNames := make([]string, 0, len(remoteByName))
	for name := range remoteByName {
		if _, declared := declaredByName[name]; !declared {
			staleNames = append(staleNames, name)
		}
	}
	sort.Strings(staleNames)
	for _, name := range staleNames {
		schedID := remoteByName[name].ID
		items = append(items, internalapi.PlanItem{
			Kind:        "schedule",
			Slug:        d.Metadata.Slug + "." + name,
			Action:      internalapi.ActionDelete,
			Description: fmt.Sprintf("delete schedule %s.%s (no longer declared)", d.Metadata.Slug, name),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return jsonDelete(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules/%s", wsID, schedID))
			},
		})
	}

	// --- 3. webhook ---
	remoteHook, err := getRoutineWebhook(ctx, c, wsID, d.Metadata.Slug)
	if err != nil {
		return nil, fmt.Errorf("routine %q: load webhook: %w", d.Metadata.Slug, err)
	}
	switch {
	case d.Spec.Webhook != nil && d.Spec.Webhook.Enabled:
		body := buildWebhookBody(d.Metadata.Slug, d.Spec.Webhook)
		if remoteHook == nil {
			items = append(items, internalapi.PlanItem{
				Kind:        "webhook",
				Slug:        d.Metadata.Slug,
				Action:      internalapi.ActionCreate,
				Description: fmt.Sprintf("create webhook for routine %s", d.Metadata.Slug),
				Exec: func(ctx context.Context, c internalapi.Client) error {
					resp, err := jsonPost(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks", wsID), body)
					if err != nil {
						return err
					}
					// Surface the resolved public URL on create. The CLI
					// captures this via OnReport (apply.go) — we cannot
					// reach OnReport directly from a kind, so the
					// returned response is logged by the Apply layer.
					_ = resp
					return nil
				},
			})
		} else if webhookDiffers(d.Spec.Webhook, remoteHook) {
			hookID := remoteHook.ID
			items = append(items, internalapi.PlanItem{
				Kind:        "webhook",
				Slug:        d.Metadata.Slug,
				Action:      internalapi.ActionUpdate,
				Description: fmt.Sprintf("update webhook for routine %s", d.Metadata.Slug),
				Exec: func(ctx context.Context, c internalapi.Client) error {
					// pipeline_webhooks has no PATCH endpoint today;
					// the update path is delete-then-recreate. We do
					// that inline so the plan item still represents one
					// logical change.
					if err := jsonDelete(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks/%s", wsID, hookID)); err != nil {
						return err
					}
					_, err := jsonPost(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks", wsID), body)
					return err
				},
			})
		} else {
			items = append(items, internalapi.PlanItem{
				Kind:        "webhook",
				Slug:        d.Metadata.Slug,
				Action:      internalapi.ActionUnchanged,
				Description: fmt.Sprintf("webhook for routine %s unchanged", d.Metadata.Slug),
			})
		}
	case remoteHook != nil:
		// Webhook declared off (or omitted entirely) but the remote
		// has one — drop it.
		hookID := remoteHook.ID
		items = append(items, internalapi.PlanItem{
			Kind:        "webhook",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionDelete,
			Description: fmt.Sprintf("delete webhook for routine %s (no longer declared)", d.Metadata.Slug),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return jsonDelete(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks/%s", wsID, hookID))
			},
		})
	}

	return items, nil
}

// ---------- diff helpers ----------

func routineDiffers(d *RoutineDocument, remote *RoutineRemote) bool {
	if d.Metadata.Name != remote.Name {
		return true
	}
	if d.Spec.Description != remote.Description {
		return true
	}
	// The strongest signal is the definition hash; compare the
	// serialized spec we'd send against the remote definition. Marshal
	// errors fall back to "differs" so the safe action is an update.
	desired, err := json.Marshal(definitionJSONShape(d))
	if err != nil {
		return true
	}
	if len(remote.DefinitionJSON) == 0 {
		return true
	}
	return !jsonEqual(desired, []byte(remote.DefinitionJSON))
}

func scheduleDiffers(declared *RoutineSchedule, remote *ScheduleRemote) bool {
	if declared.Cron != remote.CronExpr {
		return true
	}
	if declared.Timezone != remote.Timezone {
		return true
	}
	if declared.EnabledOrDefault() != remote.Enabled {
		return true
	}
	declaredInputs := declared.Inputs
	if declaredInputs == nil {
		declaredInputs = map[string]any{}
	}
	remoteInputs := remote.Inputs
	if remoteInputs == nil {
		remoteInputs = map[string]any{}
	}
	di, _ := json.Marshal(declaredInputs)
	ri, _ := json.Marshal(remoteInputs)
	return !jsonEqual(di, ri)
}

func webhookDiffers(declared *RoutineWebhook, remote *WebhookRemote) bool {
	if declared.Enabled != remote.Enabled {
		return true
	}
	return false
}

// ---------- POST body builders ----------

// buildSaveBody assembles the body for POST /pipelines/save. The
// `definition` field is the routine.v1.json DSL with `name` injected
// from metadata.slug — the server keys routines by name (its slug
// column) so the manifest slug becomes the canonical identifier.
func (d *RoutineDocument) buildSaveBody() map[string]any {
	def := definitionJSONShape(d)
	return map[string]any{
		"slug":        d.Metadata.Slug,
		"name":        d.Metadata.Name,
		"description": d.Spec.Description,
		"definition":  def,
	}
}

// definitionJSONShape strips the manifest-only fields (schedules,
// webhook) from the spec before handing the rest to pipeline.Parse on
// the server side. The DSL parser rejects unknown top-level keys, so
// we MUST omit anything the schema doesn't recognize.
func definitionJSONShape(d *RoutineDocument) map[string]any {
	out := map[string]any{
		"dsl_version": d.Spec.DSLVersion,
		"name":        d.Metadata.Slug,
		"steps":       d.Spec.Steps,
	}
	if d.Spec.Description != "" {
		out["description"] = d.Spec.Description
	}
	if len(d.Spec.Inputs) > 0 {
		out["inputs"] = d.Spec.Inputs
	}
	if len(d.Spec.CredentialsRequired) > 0 {
		out["credentials_required"] = d.Spec.CredentialsRequired
	}
	if d.Spec.EstimatedCostUSD > 0 {
		out["estimated_cost_usd"] = d.Spec.EstimatedCostUSD
	}
	if d.Spec.EstimatedDurationSeconds > 0 {
		out["estimated_duration_seconds"] = d.Spec.EstimatedDurationSeconds
	}
	if d.Spec.MaxCostUSD > 0 {
		out["max_cost_usd"] = d.Spec.MaxCostUSD
	}
	if len(d.Spec.EgressTargets) > 0 {
		out["egress_targets"] = d.Spec.EgressTargets
	}
	return out
}

// buildScheduleBody mirrors api.scheduleRequestBody. We send
// `target_pipeline_slug` so the server can resolve the FK without
// caring about the parent pipeline's id; that matches the existing UI
// flow.
func buildScheduleBody(pipelineSlug string, s *RoutineSchedule) map[string]any {
	enabled := s.EnabledOrDefault()
	body := map[string]any{
		"name":                 s.Name,
		"target_pipeline_slug": pipelineSlug,
		"cron_expr":            s.Cron,
		"timezone":             s.Timezone,
		"enabled":              enabled,
	}
	if s.Inputs != nil {
		body["inputs"] = s.Inputs
	} else {
		body["inputs"] = map[string]any{}
	}
	return body
}

// buildWebhookBody mirrors api.webhookRequestBody. The manifest
// doesn't currently carry signing_secret or rate_limit_per_min; we let
// the server pick sensible defaults and rely on `crewship routine
// webhook get` for one-time secret retrieval after create.
func buildWebhookBody(pipelineSlug string, w *RoutineWebhook) map[string]any {
	return map[string]any{
		"name":                 pipelineSlug,
		"target_pipeline_slug": pipelineSlug,
		"enabled":              w.Enabled,
	}
}

// ---------- REST helpers ----------

func saveRoutine(ctx context.Context, c internalapi.Client, wsID string, body map[string]any) error {
	_, err := jsonPost(ctx, c, fmt.Sprintf("/api/v1/workspaces/%s/pipelines/save", wsID), body)
	return err
}

func jsonPost(ctx context.Context, c internalapi.Client, path string, body any) ([]byte, error) {
	resp, err := c.Post(ctx, path, body)
	if err != nil {
		return nil, err
	}
	return readBodyChecked(resp)
}

func jsonPatch(ctx context.Context, c internalapi.Client, path string, body any) ([]byte, error) {
	resp, err := c.Patch(ctx, path, body)
	if err != nil {
		return nil, err
	}
	return readBodyChecked(resp)
}

// jsonDelete wraps c.Delete with the same status-aware check the rest
// of routine.go uses for POST/PATCH. The previous direct `c.Delete`
// call sites swallowed 4xx/5xx responses — Apply would report
// "schedule deleted" even when the server replied 500, leaving the
// row in place and the user surprised on the next reconciliation.
func jsonDelete(ctx context.Context, c internalapi.Client, path string) error {
	resp, err := c.Delete(ctx, path)
	if err != nil {
		return err
	}
	_, err = readBodyChecked(resp)
	return err
}

// readBodyChecked drains the response body (always — leaving it open
// would leak a connection) and returns an error if the status code is
// outside the 2xx range. Status-aware reading keeps the per-kind code
// from having to import net/http for the constants; we treat anything
// >=400 as an apply failure.
func readBodyChecked(resp *internalapi.Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("nil response")
	}
	var data []byte
	if resp.Body != nil {
		if rc, ok := resp.Body.(io.ReadCloser); ok {
			defer rc.Close()
		}
		buf, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		data = buf
	}
	if resp.StatusCode >= 400 {
		return data, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// listRoutineSchedules fetches every schedule in the workspace and
// keeps the ones bound to this routine's slug. The pipeline-schedules
// endpoint already returns target_pipeline_slug in the response so we
// can filter in-process without a second per-id lookup.
func listRoutineSchedules(ctx context.Context, c internalapi.Client, wsID, pipelineSlug string) ([]ScheduleRemote, error) {
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", wsID))
	if err != nil {
		return nil, err
	}
	data, err := readBodyChecked(resp)
	if err != nil {
		// 404 = workspace has no schedules. Treat as empty.
		if resp.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var all []ScheduleRemote
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("decode schedules: %w", err)
	}
	out := make([]ScheduleRemote, 0, len(all))
	for _, s := range all {
		if s.TargetPipelineSlug == pipelineSlug {
			out = append(out, s)
		}
	}
	return out, nil
}

// getRoutineWebhook returns the (at most one) webhook bound to this
// routine, or nil if none exists. Same listing strategy as schedules:
// the workspace-scoped list endpoint already includes the parent slug.
func getRoutineWebhook(ctx context.Context, c internalapi.Client, wsID, pipelineSlug string) (*WebhookRemote, error) {
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks", wsID))
	if err != nil {
		return nil, err
	}
	data, err := readBodyChecked(resp)
	if err != nil {
		if resp.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var all []WebhookRemote
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("decode webhooks: %w", err)
	}
	for i := range all {
		if all[i].TargetPipelineSlug == pipelineSlug {
			return &all[i], nil
		}
	}
	return nil, nil
}

// ---------- export ----------

// ExportRoutines reads every routine in the target workspace and
// re-assembles it as a manifest-ready RoutineDocument with schedules
// and the optional webhook nested back inside. This is the inverse of
// Plan: a freshly-applied manifest should round-trip through Export
// and produce the original document (modulo server-set fields like
// AuthorAgentID and ephemeral counters).
//
// The function makes one workspace-scoped list call for pipelines,
// one for schedules, one for webhooks, then does the cross-join in
// memory. That's three round-trips total no matter how many routines
// the workspace has — strictly better than the naive per-routine
// fetch loop.
func ExportRoutines(ctx context.Context, c internalapi.Client) ([]*RoutineDocument, error) {
	wsID := c.WorkspaceID()
	if wsID == "" {
		return nil, fmt.Errorf("workspace_id not set on client")
	}

	pipes, err := listRoutines(ctx, c, wsID)
	if err != nil {
		return nil, err
	}
	if len(pipes) == 0 {
		return nil, nil
	}
	schedsByPipeline, err := groupSchedulesByPipeline(ctx, c, wsID)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	hooksByPipeline, err := groupWebhooksByPipeline(ctx, c, wsID)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	// Build the crew_id -> slug map once so we can populate
	// metadata.labels.crew on the way back. We accept the worst case
	// where a routine's crew was deleted: the label is omitted and
	// re-applying that doc will fail Validate (no crew label) — the
	// right behavior, since the routine is in an inconsistent state.
	crewSlugByID, err := buildCrewSlugMap(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("list crews: %w", err)
	}

	out := make([]*RoutineDocument, 0, len(pipes))
	for _, p := range pipes {
		doc := &RoutineDocument{
			APIVersion: "crewship/v1",
			Kind:       "Routine",
			Metadata: internalapi.Metadata{
				Name: p.Name,
				Slug: p.Slug,
				Labels: map[string]string{
					"crew": crewSlugByID[p.AuthorCrewID],
				},
			},
		}
		// Decode the routine DSL JSON back into RoutineSpec. Unknown
		// keys land in the per-step Rest catch-all map; manifest-only
		// fields (schedules, webhook) are stitched on next.
		if len(p.DefinitionJSON) > 0 {
			var spec RoutineSpec
			if err := json.Unmarshal(p.DefinitionJSON, &spec); err != nil {
				return nil, fmt.Errorf("decode routine %s definition: %w", p.Slug, err)
			}
			doc.Spec = spec
		}
		// description is duplicated on the row itself and inside the
		// DSL; prefer the row (it's the manifest's source of truth).
		doc.Spec.Description = p.Description

		// Stitch the nested schedules/webhook.
		for _, sched := range schedsByPipeline[p.Slug] {
			enabled := sched.Enabled
			doc.Spec.Schedules = append(doc.Spec.Schedules, RoutineSchedule{
				Name:     sched.Name,
				Cron:     sched.CronExpr,
				Timezone: sched.Timezone,
				Enabled:  &enabled,
				Inputs:   sched.Inputs,
			})
		}
		if hook, ok := hooksByPipeline[p.Slug]; ok {
			doc.Spec.Webhook = &RoutineWebhook{
				Enabled: hook.Enabled,
			}
		}
		out = append(out, doc)
	}
	// Stable sort by slug — Export is used for diffs and snapshot
	// tests; the wire order should not depend on server iteration.
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Slug < out[j].Metadata.Slug })
	return out, nil
}

func listRoutines(ctx context.Context, c internalapi.Client, wsID string) ([]RoutineRemote, error) {
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v1/workspaces/%s/pipelines", wsID))
	if err != nil {
		return nil, err
	}
	data, err := readBodyChecked(resp)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	// The list response can be either a flat array or {pipelines: [...]}
	// depending on which API version is in front of us. Both shapes are
	// accepted (decodeListBody tries flat first, falls back to wrapped).
	var flat []RoutineRemote
	if err := json.Unmarshal(data, &flat); err == nil {
		// List endpoint omits `definition`; fetch each by slug to get
		// the full DSL. This is the one place we accept N+1 — the
		// definition can be many KB and including it on the list would
		// inflate every UI page render.
		out := make([]RoutineRemote, 0, len(flat))
		for _, r := range flat {
			full, err := getRoutine(ctx, c, wsID, r.Slug)
			if err != nil {
				return nil, fmt.Errorf("get routine %s: %w", r.Slug, err)
			}
			if full != nil {
				out = append(out, *full)
			}
		}
		return out, nil
	}
	var wrapped struct {
		Pipelines []RoutineRemote `json:"pipelines"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("decode pipelines list: %w", err)
	}
	out := make([]RoutineRemote, 0, len(wrapped.Pipelines))
	for _, r := range wrapped.Pipelines {
		full, err := getRoutine(ctx, c, wsID, r.Slug)
		if err != nil {
			return nil, fmt.Errorf("get routine %s: %w", r.Slug, err)
		}
		if full != nil {
			out = append(out, *full)
		}
	}
	return out, nil
}

func getRoutine(ctx context.Context, c internalapi.Client, wsID, slug string) (*RoutineRemote, error) {
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", wsID, slug))
	if err != nil {
		return nil, err
	}
	data, err := readBodyChecked(resp)
	if err != nil {
		if resp.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	var r RoutineRemote
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode routine: %w", err)
	}
	return &r, nil
}

func groupSchedulesByPipeline(ctx context.Context, c internalapi.Client, wsID string) (map[string][]ScheduleRemote, error) {
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", wsID))
	if err != nil {
		return nil, err
	}
	data, err := readBodyChecked(resp)
	if err != nil {
		if resp.StatusCode == 404 {
			return map[string][]ScheduleRemote{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string][]ScheduleRemote{}, nil
	}
	var all []ScheduleRemote
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("decode schedules: %w", err)
	}
	out := map[string][]ScheduleRemote{}
	for _, s := range all {
		out[s.TargetPipelineSlug] = append(out[s.TargetPipelineSlug], s)
	}
	// Stable order per pipeline by schedule name.
	for slug := range out {
		list := out[slug]
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		out[slug] = list
	}
	return out, nil
}

func groupWebhooksByPipeline(ctx context.Context, c internalapi.Client, wsID string) (map[string]WebhookRemote, error) {
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v1/workspaces/%s/pipeline-webhooks", wsID))
	if err != nil {
		return nil, err
	}
	data, err := readBodyChecked(resp)
	if err != nil {
		if resp.StatusCode == 404 {
			return map[string]WebhookRemote{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]WebhookRemote{}, nil
	}
	var all []WebhookRemote
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("decode webhooks: %w", err)
	}
	out := map[string]WebhookRemote{}
	for _, w := range all {
		// At most one webhook per pipeline; the last-write-wins on
		// duplicates matches the server's UNIQUE constraint behavior.
		out[w.TargetPipelineSlug] = w
	}
	return out, nil
}

func buildCrewSlugMap(ctx context.Context, c internalapi.Client) (map[string]string, error) {
	resp, err := c.Get(ctx, "/api/v1/crews")
	if err != nil {
		return nil, err
	}
	data, err := readBodyChecked(resp)
	if err != nil {
		if resp.StatusCode == 404 {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	var crews []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(data, &crews); err != nil {
		// Tolerate the wrapped {crews:[...]} shape too.
		var wrapped struct {
			Crews []struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
			} `json:"crews"`
		}
		if werr := json.Unmarshal(data, &wrapped); werr != nil {
			return nil, fmt.Errorf("decode crews: %w", err)
		}
		crews = wrapped.Crews
	}
	out := make(map[string]string, len(crews))
	for _, c := range crews {
		out[c.ID] = c.Slug
	}
	return out, nil
}

// jsonEqual compares two JSON byte slices ignoring whitespace + key
// order. Used for definition-drift checks where a re-marshalled remote
// body and a freshly-marshalled declared body should hash-equal but
// won't compare-equal as raw bytes.
func jsonEqual(a, b []byte) bool {
	var ja, jb any
	if err := json.Unmarshal(a, &ja); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &jb); err != nil {
		return false
	}
	ca, _ := json.Marshal(ja)
	cb, _ := json.Marshal(jb)
	return string(ca) == string(cb)
}
