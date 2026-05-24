package manifest

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Action enumerates the per-resource operations a plan can describe.
type Action int

const (
	ActionCreate Action = iota
	ActionUpdate
	ActionUnchanged
	ActionDelete
)

func (a Action) String() string {
	switch a {
	case ActionCreate:
		return "+"
	case ActionUpdate:
		return "~"
	case ActionUnchanged:
		return "="
	case ActionDelete:
		return "-"
	}
	return "?"
}

// PlanItem is a single line in the plan output: one resource, one
// action, one human-readable description. PlanItems are populated by
// BuildPlan and consumed by Apply.
type PlanItem struct {
	Action      Action
	Kind        string // "crew", "agent", "skill", "credential", "mcp", "agent_skill", "agent_credential"
	Description string // human label, e.g. "code-review/daniel"
	// Internal payload — used by Apply but not by the CLI's plan
	// rendering. Set by BuildPlan; opaque to the caller.
	exec func(ctx context.Context, c *Client, opts Options) error
}

// Plan is the ordered set of mutations a single apply run will
// perform. Order is significant: creates land before updates, which
// land before deletes, so a destructive sequence doesn't leave the
// workspace in a broken state if it fails partway through.
type Plan struct {
	Items []PlanItem
	// PendingCredentials carries credential env-var names that the
	// plan will leave as PENDING (no value supplied). The CLI prints
	// this list at the end so the user knows to fill values in.
	PendingCredentials []string
}

// HasDestructive returns true when the plan includes any delete.
// The apply path prompts for confirmation in that case unless the
// caller passed --yes.
func (p *Plan) HasDestructive() bool {
	for _, it := range p.Items {
		if it.Action == ActionDelete {
			return true
		}
	}
	return false
}

// Summary returns (created, updated, unchanged, deleted) counts.
func (p *Plan) Summary() (int, int, int, int) {
	var c, u, n, d int
	for _, it := range p.Items {
		switch it.Action {
		case ActionCreate:
			c++
		case ActionUpdate:
			u++
		case ActionUnchanged:
			n++
		case ActionDelete:
			d++
		}
	}
	return c, u, n, d
}

// Render returns a human-readable plan listing. Used by the CLI for
// both the pre-apply preview and (with the result tally appended)
// the final summary.
func (p *Plan) Render() string {
	var sb strings.Builder
	for _, it := range p.Items {
		fmt.Fprintf(&sb, "  %s %s %s\n", it.Action.String(), it.Kind, it.Description)
	}
	return sb.String()
}

// BuildPlan walks the manifest, fetches current workspace state via
// the client cache, and produces an ordered list of mutations that
// would converge the workspace toward the manifest. No mutations
// are issued here — BuildPlan is read-only and safe to call as
// many times as the caller likes.
func BuildPlan(ctx context.Context, c *Client, b *Bundle, opts Options) (*Plan, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	p := &Plan{}

	pb := &planBuilder{client: c, opts: opts, plan: p}
	for i := range b.Documents {
		doc := &b.Documents[i]
		if doc.Spec == nil {
			continue
		}
		if err := pb.planCrew(ctx, doc.Metadata, doc.Spec, nil, nil); err != nil {
			return nil, err
		}
	}
	for i := range b.Workspaces {
		ws := &b.Workspaces[i]
		wsCreds := indexCredentials(ws.Spec.Credentials)
		wsSkills := indexSkills(ws.Spec.Skills)
		// Workspace-scope credentials and skills.
		for j := range ws.Spec.Credentials {
			if err := pb.planCredential(ctx, &ws.Spec.Credentials[j], ""); err != nil {
				return nil, err
			}
		}
		for j := range ws.Spec.Skills {
			if err := pb.planSkill(ctx, &ws.Spec.Skills[j], "workspace"); err != nil {
				return nil, err
			}
		}
		for ci := range ws.Spec.Crews {
			crew := &ws.Spec.Crews[ci]
			meta := Metadata{
				Slug: crew.EffectiveSlug(Metadata{Slug: crew.SlugOverride}),
				Name: crew.EffectiveName(Metadata{Name: crew.Name}),
			}
			// resolve workspace-skill IDs by slug so cross-refs work
			// during apply (the exec closures need them).
			pb.workspaceCreds = wsCreds
			pb.workspaceSkillSlugs = wsSkills
			if err := pb.planCrew(ctx, meta, crew, wsCreds, wsSkills); err != nil {
				return nil, err
			}
		}
	}

	// SPEC-2 kinds — Project / Label / Routine / etc. Dispatched
	// through internal/manifest/kinds, with the action mapping +
	// closure wrapping in apply_kinds.go.
	if err := pb.planNewKinds(ctx, b); err != nil {
		return nil, err
	}

	// Sort: bucket by action (create / update / unchanged / delete)
	// then by dependency-aware kind order within each bucket so
	// children land after parents on create, and parents land after
	// children on delete.
	sort.SliceStable(p.Items, func(i, j int) bool {
		a, b := p.Items[i], p.Items[j]
		if a.Action != b.Action {
			return planActionOrder(a.Action) < planActionOrder(b.Action)
		}
		ka := kindOrder(a.Kind, a.Action)
		kb := kindOrder(b.Kind, b.Action)
		if ka != kb {
			return ka < kb
		}
		return a.Description < b.Description
	})

	return p, nil
}

// planActionOrder controls the bucket ordering of plan items.
// Creates first so dependent resources land before the things that
// link them; deletes last so we don't drop a row another step
// still needs.
func planActionOrder(a Action) int {
	switch a {
	case ActionCreate:
		return 0
	case ActionUpdate:
		return 1
	case ActionUnchanged:
		return 2
	case ActionDelete:
		return 3
	}
	return 4
}

// kindOrder ranks kinds within an action bucket so dependencies
// resolve. On create: parents (crew, credential, skill) before
// children (agent, mcp) before links (agent_skill, agent_credential).
// On delete: reverse — links first, then children, then parents.
func kindOrder(kind string, action Action) int {
	rank := map[string]int{
		// Legacy crew-bundle kinds (lowercase = manifest internals).
		"credential":       0,
		"skill":            1,
		"crew":             2,
		"service":          3, // sidecar services land alongside agents
		"agent":            4,
		"mcp":              5,
		"agent_skill":      6,
		"agent_credential": 6,

		// SPEC-2 kinds (capitalised = doc-kind names emitted by the
		// per-kind packages under internal/manifest/kinds). Order
		// mirrors the topological phases documented in SPEC-2:
		// foundations first, automation last, hooks at the very end.
		"Project":          10,
		"Label":            10, // parallel to Project — neither depends on the other
		"Skill":            10, // parallel — agents reference, but agents live in legacy bundle
		"Milestone":        11, // depends on Project
		"WorkflowTemplate": 12,
		"FeatureFlag":      13,
		"InstanceSetting":  14,
		"Recipe":           15, // catalog installs (need workspace creds in place)
		"CrewTemplate":     16,
		"Connector":        17,
		"Routine":          18, // depends on Crew + Agent
		"Schedule":         19, // nested under Routine
		"Webhook":          19,
		"RecurringIssue":   20, // depends on Project + Label + Crew
		"Issue":            20, // depends on Crew + optional Project / Agent / Labels
		"TriageRule":       21, // depends on Project + Label + Crew
		"SavedView":        22, // depends on Label + Project
		"Hook":             23, // toggles existing rows
	}
	r, ok := rank[kind]
	if !ok {
		return 99
	}
	if action == ActionDelete {
		// Reverse the order so we tear down links before agents,
		// agents before crews — and SPEC-2 kinds tear down before
		// their FK parents. The constant is large enough to keep new
		// kinds (10..23) below the unknown-kind fallback (99) even
		// after reversal.
		return 100 - r
	}
	return r
}

// planBuilder is the mutable scratch space used while assembling a
// plan. It carries the open client (so we can fetch state) and the
// growing plan.
type planBuilder struct {
	client *Client
	opts   Options
	plan   *Plan

	// Workspace-scope state available to nested crew specs.
	workspaceCreds      map[string]Credential
	workspaceSkillSlugs map[string]Skill
}

func (pb *planBuilder) appendItem(action Action, kind, desc string, exec func(ctx context.Context, c *Client, opts Options) error) {
	pb.plan.Items = append(pb.plan.Items, PlanItem{
		Action:      action,
		Kind:        kind,
		Description: desc,
		exec:        exec,
	})
}

func (pb *planBuilder) planCredential(ctx context.Context, cred *Credential, crewID string) error {
	c := pb.client
	existing, err := c.FindCredentialByName(ctx, cred.EnvVar)
	if err != nil {
		return fmt.Errorf("look up credential %q: %w", cred.EnvVar, err)
	}
	credCopy := *cred
	if existing != nil {
		// Manifest never overwrites credential values. Just record
		// the existing status so the user knows whether their slot
		// is still pending.
		statusNote := existing.Status
		if statusNote == "" {
			statusNote = "ACTIVE"
		}
		if statusNote == "PENDING" {
			pb.plan.PendingCredentials = append(pb.plan.PendingCredentials, cred.EnvVar)
		}
		pb.appendItem(ActionUnchanged, "credential",
			fmt.Sprintf("%s (%s, %s)", cred.EnvVar, cred.Provider, statusNote),
			nil)
		return nil
	}
	pb.appendItem(ActionCreate, "credential",
		fmt.Sprintf("%s (%s)", cred.EnvVar, cred.Provider),
		func(ctx context.Context, client *Client, opts Options) error {
			body := map[string]any{
				"name":     credCopy.EnvVar,
				"type":     credCopy.Type,
				"provider": credCopy.Provider,
				"scope":    "WORKSPACE",
			}
			if crewID != "" {
				body["scope"] = "CREW"
				body["crew_id"] = crewID
			}
			if credCopy.Description != "" {
				body["description"] = credCopy.Description
			}
			if credCopy.Label != "" {
				body["account_label"] = credCopy.Label
			}
			if value, ok := opts.Secrets.ValueFor(credCopy.EnvVar); ok {
				body["value"] = value
			} else {
				body["pending"] = true
			}
			_, err := client.CreateCredential(ctx, body)
			return err
		})
	// Track pending list at plan time so the CLI can warn about it
	// even before the create runs.
	if pb.opts.Secrets != nil {
		if _, ok := pb.opts.Secrets.ValueFor(cred.EnvVar); !ok {
			pb.plan.PendingCredentials = append(pb.plan.PendingCredentials, cred.EnvVar)
		}
	} else {
		pb.plan.PendingCredentials = append(pb.plan.PendingCredentials, cred.EnvVar)
	}
	return nil
}

func (pb *planBuilder) planSkill(ctx context.Context, s *Skill, scope string) error {
	skill := *s
	skill.SetResolved(s.Resolved())
	pb.appendItem(ActionCreate, "skill",
		fmt.Sprintf("%s (%s)", skill.Slug, scope),
		func(ctx context.Context, client *Client, opts Options) error {
			body := map[string]any{}
			switch {
			case skill.Inline != "" || skill.Path != "":
				if r := skill.Resolved(); r != "" {
					body["content"] = r
				} else {
					return fmt.Errorf("skill %q: body not resolved (call LoadFile or supply inline content)", skill.Slug)
				}
			case skill.Source != "":
				body["url"] = skill.Source
			default:
				return fmt.Errorf("skill %q: no resolvable source", skill.Slug)
			}
			if skill.AllowUnsafeLicense {
				body["allow_unsafe_license"] = true
			}
			_, err := client.ImportSkill(ctx, body)
			return err
		})
	return nil
}

func (pb *planBuilder) planCrew(ctx context.Context, meta Metadata, spec *CrewSpec, wsCreds map[string]Credential, wsSkills map[string]Skill) error {
	slug := spec.EffectiveSlug(meta)
	name := spec.EffectiveName(meta)

	existing, err := pb.client.FindCrewBySlug(ctx, slug)
	if err != nil {
		return err
	}

	// Auto-managed sidecar credentials: expand IN-PLACE on a deep
	// enough copy that we don't mutate the caller's manifest. The
	// expansion writes generated values into the services' Env map
	// and appends credential names to every agent's env_refs in the
	// crew. On a re-apply, the existing services_json carries the
	// previously-generated values; passing it in lets expand reuse
	// them so the credential row and the sidecar env stay in lockstep
	// across consecutive applies.
	specCopy := deepCopyCrewSpec(spec)
	var existingServicesJSON string
	if existing != nil && existing.ServicesJSON != nil {
		existingServicesJSON = *existing.ServicesJSON
	}
	plannedAutoCreds, err := expandAutoCredentialsInCrewSpec(&specCopy, existingServicesJSON)
	if err != nil {
		return fmt.Errorf("crew %q: auto-managed credentials: %w", slug, err)
	}

	// Cross-crew collision check: a workspace-scoped credential
	// already exists under one of our planned names. Two outcomes
	// matter — if the existing row IS the same auto-managed
	// (matching provisioned_for_service tag), we no-op later via
	// idempotency. If it's a different crew's auto-managed row, or
	// an operator's manual credential, that's a real conflict and
	// the operator has to rename.
	for i := range plannedAutoCreds {
		ac := &plannedAutoCreds[i]
		existingCred, lookErr := pb.client.FindCredentialByName(ctx, ac.Name)
		if lookErr != nil {
			return fmt.Errorf("crew %q: lookup credential %q: %w", slug, ac.Name, lookErr)
		}
		if existingCred == nil {
			continue
		}
		wantTag := slug + "/" + ac.ProvisionedForService
		if existingCred.Provider == "AUTO_MANAGED" &&
			existingCred.ProvisionedForService != nil &&
			*existingCred.ProvisionedForService == wantTag {
			continue // same crew, same service — re-apply idempotency handles it
		}
		owner := "operator-managed"
		if existingCred.Provider == "AUTO_MANAGED" && existingCred.ProvisionedForService != nil {
			owner = "auto-managed for " + *existingCred.ProvisionedForService
		}
		return fmt.Errorf(
			"crew %q: auto_credential %q clashes with an existing workspace credential (%s); "+
				"rename the auto_credential (e.g. %s_%s) to keep workspace names unique",
			slug, ac.Name, owner, ac.Name, strings.ToUpper(strings.ReplaceAll(slug, "-", "_")))
	}

	crewBody, err := buildCrewBody(name, slug, &specCopy)
	if err != nil {
		return fmt.Errorf("crew %q: %w", slug, err)
	}
	var crewIDForChildren string

	switch {
	case existing == nil && pb.opts.Mode == ApplyStrict:
		// Strict mode treats "missing" as fine; the strict gate is
		// for collisions, not absences. Plan a create.
		fallthrough
	case existing == nil:
		pb.appendItem(ActionCreate, "crew", slug,
			func(ctx context.Context, client *Client, opts Options) error {
				_, err := client.CreateCrew(ctx, crewBody)
				return err
			})
	case pb.opts.Mode == ApplyStrict:
		return fmt.Errorf("crew %q already exists (drop --strict to update in place, or pass --replace to recreate)", slug)
	case pb.opts.Mode == ApplyReplace:
		existingID := existing.ID
		pb.appendItem(ActionDelete, "crew", slug+" (replace)",
			func(ctx context.Context, client *Client, opts Options) error {
				return client.DeleteCrew(ctx, existingID)
			})
		pb.appendItem(ActionCreate, "crew", slug,
			func(ctx context.Context, client *Client, opts Options) error {
				_, err := client.CreateCrew(ctx, crewBody)
				return err
			})
	default:
		// Upsert: diff body vs existing. If a structural field
		// differs, plan an update; otherwise mark unchanged.
		// PATCH ignores slug so strip before sending.
		updateBody := copyMap(crewBody)
		delete(updateBody, "slug")
		if crewBodyDiffers(existing, crewBody) {
			existingID := existing.ID
			pb.appendItem(ActionUpdate, "crew", slug,
				func(ctx context.Context, client *Client, opts Options) error {
					_, err := client.UpdateCrew(ctx, existingID, updateBody)
					return err
				})
		} else {
			pb.appendItem(ActionUnchanged, "crew", slug, nil)
		}
		crewIDForChildren = existing.ID
	}

	// Crew-scope credentials. crewID is unknown at plan-time for
	// freshly-created crews; we pass "" so the exec closure goes
	// workspace-scope. A future iteration may emit two-phase plans
	// where crew-scope creds wait on the crew to land first.
	for i := range specCopy.Credentials {
		// The Credential may be intended as crew-scoped, but for V1
		// we always create it workspace-scoped since plan-time IDs
		// are not available for new crews. Document this trade in
		// the manifest README.
		if err := pb.planCredential(ctx, &specCopy.Credentials[i], ""); err != nil {
			return err
		}
	}

	// Crew-scope skills.
	for i := range specCopy.Skills {
		if err := pb.planSkill(ctx, &specCopy.Skills[i], "crew "+slug); err != nil {
			return err
		}
	}

	// Sidecar services land alongside the crew. Each one becomes a
	// plan entry so the diff is visible; the actual create happens
	// inside buildCrewBody which serialises spec.Services into the
	// crew's services_json column. The docker provider reads that
	// column at EnsureCrewRuntime time and starts the sidecars.
	for i := range specCopy.Services {
		s := &specCopy.Services[i]
		pb.appendItem(ActionCreate, "service",
			fmt.Sprintf("%s/%s (%s)", slug, s.Name, s.Image),
			nil)
	}

	// MCP servers, agents — only plan their child operations if we
	// know the existing crew ID. For brand-new crews we plan an
	// "exec deferred" wrapper that resolves the ID at apply time by
	// re-fetching the crew list (the cache is invalidated by the
	// create).
	if err := pb.planCrewChildren(ctx, slug, crewIDForChildren, &specCopy, wsCreds, wsSkills); err != nil {
		return err
	}

	// Auto-managed sidecar credentials (v98). One plan item per
	// (service, auto-credential) tuple. Each runs AFTER the crew +
	// agents are created so the closure can pin the LEAD agent for
	// attribution and the credential row's `created_by_actor_id`.
	// Idempotent: if a row with the same name+workspace already
	// exists with provider=AUTO_MANAGED, the closure no-ops.
	if err := pb.planAutoManagedCredentials(ctx, slug, plannedAutoCreds); err != nil {
		return err
	}
	return nil
}

// planAutoManagedCredentials emits one plan item per generated
// auto-credential. The closure that runs at apply time:
//
//  1. Checks workspace credentials for an existing AUTO_MANAGED row
//     with the same name; on match, no-ops (re-apply idempotency).
//  2. POSTs a fresh credential with provider=AUTO_MANAGED, status=
//     ACTIVE, created_by_actor_type='system' (v98 MVP), and
//     provisioned_for_service=<crew>/<service>.
//
// Attribution is intentionally 'system' (not 'agent') for v98. The
// row's provisioned_for_service tag is the load-bearing audit signal
// — UI surfaces "Auto-managed for <crew>/<service>" from it, and a
// follow-up patch can hydrate the attributed agent once the
// rank-ordering chicken-and-egg with agent creation is resolved.
// See SPEC-4 §"Two-phase attribution" for the deferred design.
//
// Plan output renders one row per credential ("create credential
// POSTGRES_PASSWORD for uo-outlands/postgres"). The generated value
// is intentionally NOT in that string — captured inside the closure.
//
// Kind is "credential" so these items rank with normal credentials
// (well before agents that env_ref them).
func (pb *planBuilder) planAutoManagedCredentials(ctx context.Context, crewSlug string, planned []plannedAutoCredential) error {
	for i := range planned {
		ac := planned[i] // local capture; closure must not share loop var
		provServiceTag := crewSlug + "/" + ac.ProvisionedForService
		summary := fmt.Sprintf("%s (auto-managed for %s/%s)", ac.Name, crewSlug, ac.ProvisionedForService)

		// Plan-time prediction. A re-apply against a workspace that
		// already carries the matching auto-managed credential is a
		// no-op — same name, same provider, same provisioned_for_service
		// tag, same value (expandAutoCredentialsInCrewSpec reused the
		// prior value via reuseOrGenerate). Reporting that as
		// "+ credential" + "1 created" was misleading: the row count
		// in the plan footer never matched what actually changed in
		// the DB. Look up the workspace's current credentials so the
		// plan output matches reality.
		//
		// Errors from FindCredentialByName propagate up — pre-CodeRabbit
		// this swallowed the err and treated the lookup as "no existing
		// credential", which meant a transient server-down or network
		// failure during planning would silently flip every auto-managed
		// row to ActionCreate. Apply would then start, hit the closure's
		// repeat lookup, and either succeed (confusingly contradicting
		// the plan) or fail mid-execution. Surfacing the read error at
		// plan time keeps the contract that BuildPlan is "see the whole
		// shape before any mutation runs".
		//
		// The closure below still runs the same provenance check —
		// it remains load-bearing as a TOCTOU defence and as the
		// authoritative path that returns nil-or-error.
		predicted := ActionCreate
		existing, err := pb.client.FindCredentialByName(ctx, ac.Name)
		if err != nil {
			return fmt.Errorf("plan auto-managed %s: lookup existing: %w", ac.Name, err)
		}
		if existing != nil &&
			existing.Provider == "AUTO_MANAGED" &&
			existing.ProvisionedForService != nil &&
			*existing.ProvisionedForService == provServiceTag {
			predicted = ActionUnchanged
		}

		pb.appendItem(predicted, "credential", summary,
			func(ctx context.Context, client *Client, opts Options) error {
				existing, err := client.FindCredentialByName(ctx, ac.Name)
				if err != nil {
					return fmt.Errorf("auto-managed %s: lookup existing: %w", ac.Name, err)
				}
				if existing != nil {
					// The plan-time check in planCrew already rejects
					// cross-crew collisions, but TOCTOU between plan
					// and exec is real: another apply, a UI mutate,
					// or a race in CI could materialise a colliding
					// row in the meantime. Re-check provenance here
					// — same tag is idempotent, anything else is a
					// real conflict the dispatch must not paper over.
					if existing.Provider == "AUTO_MANAGED" &&
						existing.ProvisionedForService != nil &&
						*existing.ProvisionedForService == provServiceTag {
						return nil
					}
					if existing.Provider == "AUTO_MANAGED" {
						otherTag := "(no provisioned_for_service)"
						if existing.ProvisionedForService != nil {
							otherTag = *existing.ProvisionedForService
						}
						return fmt.Errorf(
							"auto-managed %s: name collides with another AUTO_MANAGED credential bound to %s; "+
								"workspace credential names must be unique per service",
							ac.Name, otherTag)
					}
					return fmt.Errorf(
						"auto-managed %s: credential already exists with provider=%s; "+
							"delete it manually or rename the auto-credential to resolve the conflict",
						ac.Name, existing.Provider)
				}
				body := map[string]any{
					"name":                    ac.Name,
					"value":                   ac.Value,
					"type":                    "GENERIC_SECRET",
					"provider":                "AUTO_MANAGED",
					"scope":                   "WORKSPACE",
					"created_by_actor_type":   "system",
					"provisioned_for_service": provServiceTag,
				}
				if ac.Description != "" {
					body["description"] = ac.Description
				}
				if _, err := client.CreateCredential(ctx, body); err != nil {
					return fmt.Errorf("auto-managed %s: create: %w", ac.Name, err)
				}
				return nil
			})
	}
	return nil
}

// planCrewChildren emits plan items for MCP servers + agents + their
// cross-refs. When the parent crew is new (crewIDForChildren == ""),
// the exec closures look up the crew by slug at apply-time.
//
// Errors from the list-state API calls are now propagated up rather
// than swallowed. Previously a transient ListCrewIntegrations
// failure would turn into "no existing MCPs" and the plan would
// confidently emit creates for resources that already exist,
// producing 409s at apply time with no upstream signal that the
// plan was based on partial state.
func (pb *planBuilder) planCrewChildren(ctx context.Context, crewSlug, crewID string, spec *CrewSpec, wsCreds map[string]Credential, wsSkills map[string]Skill) error {
	// MCP servers
	var existingMCPs []MCPServerResponse
	if crewID != "" {
		mcps, err := pb.client.ListCrewIntegrations(ctx, crewID)
		if err != nil {
			return fmt.Errorf("list mcp integrations for crew %q: %w", crewSlug, err)
		}
		existingMCPs = mcps
	}
	mcpExisting := map[string]MCPServerResponse{}
	for _, m := range existingMCPs {
		mcpExisting[m.Name] = m
	}
	mcpDeclared := map[string]bool{}
	for i := range spec.MCPServers {
		mcp := spec.MCPServers[i]
		mcpDeclared[mcp.Name] = true
		body := buildMCPBody(&mcp)
		if existing, ok := mcpExisting[mcp.Name]; ok {
			if mcpBodyDiffers(&existing, &mcp) {
				// MCP integrations don't expose a PATCH endpoint
				// at the server today — the only way to "update"
				// an MCP server config is delete + recreate, which
				// would interrupt the running agent. We surface
				// drift as an Unchanged-with-warning entry so the
				// operator sees the mismatch in the plan and can
				// decide to re-create manually, without misleading
				// them with an ActionUpdate that has no exec
				// closure attached.
				pb.appendItem(ActionUnchanged, "mcp",
					fmt.Sprintf("%s/%s (drift detected — server keeps existing config; recreate manually if needed)", crewSlug, mcp.Name),
					nil)
			} else {
				pb.appendItem(ActionUnchanged, "mcp", crewSlug+"/"+mcp.Name, nil)
			}
		} else {
			pb.appendItem(ActionCreate, "mcp", crewSlug+"/"+mcp.Name,
				func(ctx context.Context, client *Client, opts Options) error {
					id := crewID
					if id == "" {
						crew, _ := client.FindCrewBySlug(ctx, crewSlug)
						if crew == nil {
							return fmt.Errorf("crew %q not found at apply time", crewSlug)
						}
						id = crew.ID
					}
					return client.CreateCrewIntegration(ctx, id, body)
				})
		}
	}
	// Sync: queue deletes for MCPs the manifest doesn't declare.
	if pb.opts.Mode == ApplyUpsert {
		for name, m := range mcpExisting {
			if mcpDeclared[name] {
				continue
			}
			id := m.ID
			pb.appendItem(ActionDelete, "mcp", crewSlug+"/"+name,
				func(ctx context.Context, client *Client, opts Options) error {
					return client.DeleteCrewIntegration(ctx, crewID, id)
				})
		}
	}

	// Agents
	var existingAgents []AgentResponse
	if crewID != "" {
		agents, err := pb.client.ListAgentsByCrew(ctx, crewID)
		if err != nil {
			return fmt.Errorf("list agents for crew %q: %w", crewSlug, err)
		}
		existingAgents = agents
	}
	agentExisting := map[string]AgentResponse{}
	for _, a := range existingAgents {
		agentExisting[a.Slug] = a
	}
	agentDeclared := map[string]bool{}
	for i := range spec.Agents {
		a := spec.Agents[i]
		agentDeclared[a.Slug] = true
		desc := crewSlug + "/" + a.Slug
		body := buildAgentBody(&a, crewID, crewSlug)
		if existing, ok := agentExisting[a.Slug]; ok {
			updateBody := copyMap(body)
			delete(updateBody, "slug")
			delete(updateBody, "crew_id")
			if agentBodyDiffers(&existing, &a) {
				existingID := existing.ID
				pb.appendItem(ActionUpdate, "agent", desc,
					func(ctx context.Context, client *Client, opts Options) error {
						_, err := client.UpdateAgent(ctx, existingID, updateBody)
						return err
					})
			} else {
				pb.appendItem(ActionUnchanged, "agent", desc, nil)
			}
			if err := pb.planAgentLinks(ctx, existing.ID, &a, wsCreds, wsSkills, crewSlug); err != nil {
				return err
			}
		} else {
			agentCopy := a
			pb.appendItem(ActionCreate, "agent", desc,
				func(ctx context.Context, client *Client, opts Options) error {
					// Resolve crewID lazily for new crews.
					body := buildAgentBody(&agentCopy, crewID, crewSlug)
					if body["crew_id"] == "" {
						crew, _ := client.FindCrewBySlug(ctx, crewSlug)
						if crew == nil {
							return fmt.Errorf("crew %q not found at apply time", crewSlug)
						}
						body["crew_id"] = crew.ID
					}
					created, err := client.CreateAgent(ctx, body)
					if err != nil {
						return err
					}
					return applyAgentRefs(ctx, client, created.ID, &agentCopy, wsCreds, wsSkills)
				})
		}
	}
	// Sync: agents removed from manifest get deleted.
	if pb.opts.Mode == ApplyUpsert {
		for slug, a := range agentExisting {
			if agentDeclared[slug] {
				continue
			}
			id := a.ID
			pb.appendItem(ActionDelete, "agent", crewSlug+"/"+slug,
				func(ctx context.Context, client *Client, opts Options) error {
					return client.DeleteAgent(ctx, id, crewID)
				})
		}
	}
	return nil
}

// planAgentLinks emits unchanged/create/delete entries for an
// existing agent's skill and credential bindings so the report
// reflects diff churn even when the agent row itself is unchanged.
//
// The actual link mutations happen inside the agent's exec closure
// during apply (createAgent path) or via these helpers when the
// agent already exists.
func (pb *planBuilder) planAgentLinks(ctx context.Context, agentID string, a *Agent, wsCreds map[string]Credential, wsSkills map[string]Skill, crewSlug string) error {
	existingSkills, err := pb.client.ListAgentSkills(ctx, agentID)
	if err != nil {
		return fmt.Errorf("list agent skills for %q/%q: %w", crewSlug, a.Slug, err)
	}
	existingSkillSlugs := map[string]string{}
	for _, b := range existingSkills {
		existingSkillSlugs[b.Skill.Slug] = b.SkillID
	}
	declaredSkillSlugs := map[string]bool{}
	for _, slug := range a.Skills {
		declaredSkillSlugs[slug] = true
		if _, exists := existingSkillSlugs[slug]; !exists {
			s := slug
			pb.appendItem(ActionCreate, "agent_skill", crewSlug+"/"+a.Slug+":"+slug,
				func(ctx context.Context, client *Client, opts Options) error {
					all, _ := client.ListSkills(ctx)
					var skillID string
					for _, sk := range all {
						if sk.Slug == s {
							skillID = sk.ID
							break
						}
					}
					if skillID == "" {
						return fmt.Errorf("skill %q not found at apply time", s)
					}
					return client.AddSkillToAgent(ctx, agentID, skillID)
				})
		}
	}
	// Drift: skills bound on the agent but no longer declared.
	if pb.opts.Mode == ApplyUpsert {
		for slug, skillID := range existingSkillSlugs {
			if declaredSkillSlugs[slug] {
				continue
			}
			id := skillID
			pb.appendItem(ActionDelete, "agent_skill", crewSlug+"/"+a.Slug+":"+slug,
				func(ctx context.Context, client *Client, opts Options) error {
					return client.RemoveSkillFromAgent(ctx, agentID, id)
				})
		}
	}

	existingCreds, err := pb.client.ListAgentCredentials(ctx, agentID)
	if err != nil {
		return fmt.Errorf("list agent credentials for %q/%q: %w", crewSlug, a.Slug, err)
	}
	existingCredsByEnv := map[string]AgentCredentialBinding{}
	for _, b := range existingCreds {
		existingCredsByEnv[b.EnvVarName] = b
	}
	declaredCredEnvs := map[string]bool{}
	for _, env := range a.EnvRefs {
		declaredCredEnvs[env] = true
		if _, exists := existingCredsByEnv[env]; !exists {
			envName := env
			pb.appendItem(ActionCreate, "agent_credential", crewSlug+"/"+a.Slug+":"+env,
				func(ctx context.Context, client *Client, opts Options) error {
					cr, err := client.FindCredentialByName(ctx, envName)
					if err != nil {
						return err
					}
					if cr == nil {
						return fmt.Errorf("credential %q not found at apply time", envName)
					}
					return client.LinkCredentialToAgent(ctx, agentID, cr.ID, envName)
				})
		}
	}
	if pb.opts.Mode == ApplyUpsert {
		for env, binding := range existingCredsByEnv {
			if declaredCredEnvs[env] {
				continue
			}
			assignmentID := binding.ID
			pb.appendItem(ActionDelete, "agent_credential", crewSlug+"/"+a.Slug+":"+env,
				func(ctx context.Context, client *Client, opts Options) error {
					return client.RemoveCredentialFromAgent(ctx, agentID, assignmentID)
				})
		}
	}
	return nil
}

// applyAgentRefs links a freshly-created agent's skills and
// credentials. Called by the create-agent exec closure.
func applyAgentRefs(ctx context.Context, c *Client, agentID string, a *Agent, wsCreds map[string]Credential, wsSkills map[string]Skill) error {
	allSkills, err := c.ListSkills(ctx)
	if err != nil {
		return err
	}
	skillsBySlug := map[string]string{}
	for _, sk := range allSkills {
		skillsBySlug[sk.Slug] = sk.ID
	}
	for _, slug := range a.Skills {
		id := skillsBySlug[slug]
		if id == "" {
			return fmt.Errorf("skill %q not found", slug)
		}
		if err := c.AddSkillToAgent(ctx, agentID, id); err != nil {
			return err
		}
	}
	for _, env := range a.EnvRefs {
		cr, err := c.FindCredentialByName(ctx, env)
		if err != nil {
			return err
		}
		if cr == nil {
			return fmt.Errorf("credential %q not found", env)
		}
		if err := c.LinkCredentialToAgent(ctx, agentID, cr.ID, env); err != nil {
			return err
		}
	}
	return nil
}

// crewBodyDiffers returns true when the manifest body's structural
// fields don't match the existing crew. Only fields the manifest
// can drive are compared — server-managed fields (created_at,
// cached_image, etc.) are excluded.
//
// Historical note: services_json + container limits + network policy
// used to be missing from this comparison. The symptom: an operator
// would edit a manifest (add a sidecar, change cpus, scope the
// network), re-apply, and see "0 updated" while the server still ran
// the old config. With services_json in particular, the SPEC-4 sugar
// auto-credential rotation never propagated because the patch wasn't
// sent at all — the server's UpdateCrew handler is the place that
// invalidates `cached_image` on services_json drift, so a missing
// PATCH meant a stale agent runtime forever. The field set below is
// the subset of CrewResponse that buildCrewBody actually drives;
// anything new added to buildCrewBody must be mirrored here.
func crewBodyDiffers(existing *CrewResponse, body map[string]any) bool {
	if v, ok := body["name"].(string); ok && v != existing.Name {
		return true
	}
	if v, ok := body["description"].(string); ok && deref(existing.Description) != v {
		return true
	}
	if v, ok := body["color"].(string); ok && deref(existing.Color) != v {
		return true
	}
	if v, ok := body["icon"].(string); ok && deref(existing.Icon) != v {
		return true
	}
	if v, ok := body["runtime_image"].(string); ok && deref(existing.RuntimeImage) != v {
		return true
	}
	if v, ok := body["devcontainer_config"].(string); ok && deref(existing.DevcontainerConfig) != v {
		return true
	}
	if v, ok := body["mise_config"].(string); ok && deref(existing.MiseConfig) != v {
		return true
	}
	if v, ok := body["services_json"].(string); ok && deref(existing.ServicesJSON) != v {
		return true
	}
	// Container shape and network policy. nil-deref for *int / *float64
	// returns the zero value, which is fine: if the manifest is silent
	// (no devcontainer block at all) buildCrewBody doesn't set these
	// keys, so the map lookup fails and we skip the check.
	if v, ok := body["container_memory_mb"].(int); ok && derefInt(existing.ContainerMemoryMB) != v {
		return true
	}
	if v, ok := body["container_cpus"].(float64); ok && derefFloat(existing.ContainerCPUs) != v {
		return true
	}
	if v, ok := body["container_ttl_hours"].(int); ok && derefInt(existing.ContainerTTLHours) != v {
		return true
	}
	if v, ok := body["network_mode"].(string); ok && deref(existing.NetworkMode) != v {
		return true
	}
	if v, ok := body["allowed_domains"].([]string); ok && !stringSliceEq(existing.AllowedDomains, v) {
		return true
	}
	return false
}

// derefInt / derefFloat / stringSliceEq are the small helpers
// crewBodyDiffers needs but the deref helper above only handles
// *string. Kept package-private so they don't pollute the public
// surface; collocated with their only caller.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func agentBodyDiffers(existing *AgentResponse, a *Agent) bool {
	if a.Name != existing.Name {
		return true
	}
	if a.AgentRole != "" && a.AgentRole != existing.AgentRole {
		return true
	}
	if a.CLIAdapter != "" && a.CLIAdapter != existing.CLIAdapter {
		return true
	}
	if a.LLM.Provider != "" && a.LLM.Provider != deref(existing.LLMProvider) {
		return true
	}
	if a.LLM.Model != "" && a.LLM.Model != deref(existing.LLMModel) {
		return true
	}
	if a.ToolProfile != "" && a.ToolProfile != existing.ToolProfile {
		return true
	}
	if a.TimeoutSeconds != 0 && a.TimeoutSeconds != existing.TimeoutSeconds {
		return true
	}
	if a.MemoryEnabled != existing.MemoryEnabled {
		return true
	}
	if a.Prompt != deref(existing.SystemPrompt) {
		return true
	}
	if a.RoleTitle != "" && a.RoleTitle != deref(existing.RoleTitle) {
		return true
	}
	return false
}

func mcpBodyDiffers(existing *MCPServerResponse, m *MCPServer) bool {
	if m.Transport != existing.Transport {
		return true
	}
	if m.Command != "" && m.Command != deref(existing.Command) {
		return true
	}
	if m.Endpoint != "" && m.Endpoint != deref(existing.Endpoint) {
		return true
	}
	return false
}

func buildAgentBody(a *Agent, crewID, crewSlug string) map[string]any {
	body := map[string]any{
		"name":            a.Name,
		"slug":            a.Slug,
		"crew_id":         crewID,
		"agent_role":      defaultStr(a.AgentRole, "AGENT"),
		"cli_adapter":     defaultStr(a.CLIAdapter, "CLAUDE_CODE"),
		"tool_profile":    defaultStr(a.ToolProfile, "CODING"),
		"memory_enabled":  a.MemoryEnabled,
		"timeout_seconds": defaultInt(a.TimeoutSeconds, 1800),
	}
	if a.RoleTitle != "" {
		body["role_title"] = a.RoleTitle
	}
	if a.Description != "" {
		body["description"] = a.Description
	}
	if a.LeadMode != "" {
		body["lead_mode"] = a.LeadMode
	}
	if a.LLM.Provider != "" {
		body["llm_provider"] = a.LLM.Provider
	}
	if a.LLM.Model != "" {
		body["llm_model"] = a.LLM.Model
	}
	if a.Prompt != "" {
		body["system_prompt"] = a.Prompt
	}
	return body
}

func buildMCPBody(s *MCPServer) map[string]any {
	body := map[string]any{
		"name":         s.Name,
		"display_name": defaultStr(s.DisplayName, s.Name),
		"transport":    s.Transport,
		"enabled":      true,
	}
	if s.Enabled != nil {
		body["enabled"] = *s.Enabled
	}
	if s.Command != "" {
		body["command"] = s.Command
	}
	if len(s.Args) > 0 {
		body["args"] = s.Args
	}
	if s.Endpoint != "" {
		body["endpoint"] = s.Endpoint
	}
	if s.Icon != "" {
		body["icon"] = s.Icon
	}
	if len(s.EnvMapping) > 0 {
		body["env_mapping"] = s.EnvMapping
	}
	return body
}
