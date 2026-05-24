package manifest

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"github.com/crewship-ai/crewship/internal/manifest/kinds"
)

// apply_kinds.go is the dispatch glue between the legacy Crew/Workspace
// BuildPlan loop in plan.go and the SPEC-2 kinds package under
// internal/manifest/kinds. Each new kind owns its own Plan() method
// returning internalapi.PlanItem values; this file:
//
//   1. Builds a WorkspaceContext so cross-kind FK validators see every
//      declared entity (a Milestone in the same file as its Project
//      doesn't 404 at validate time just because the project hasn't
//      been created on the server yet).
//   2. Runs kind.Validate() over every document for fail-fast errors.
//   3. Walks declared kinds in SPEC-2 phase order, calls kind.Plan(),
//      and converts the resulting internalapi.PlanItem entries into the
//      manifest.PlanItem shape the rest of the apply pipeline already
//      knows how to render and execute.
//
// Per-kind Plan signatures vary slightly (some accept a pre-fetched
// remote, some look up internally). Each switch arm below knows how
// to drive its kind — the small repetition is the price of not
// shoehorning 14 different shapes through a single reflective
// dispatcher, which would obscure what each kind actually does.

// planNewKinds appends plan items for every SPEC-2 kind declared in
// the bundle to the existing plan builder. Called from BuildPlan
// after the legacy crew/workspace iteration finishes — order matters
// inside this function (project before milestone, etc.) but the
// outer plan sorter (kindOrder in plan.go) does the final action +
// dependency ordering.
func (pb *planBuilder) planNewKinds(ctx context.Context, b *Bundle) error {
	if pb.client == nil {
		return fmt.Errorf("manifest: planBuilder has nil client (programmer error)")
	}
	wsCtx := buildKindWorkspaceContext(b)
	if err := validateAllKinds(b, wsCtx); err != nil {
		return err
	}
	c := newInternalClient(pb.client)

	// Phase 3: Projects (no deps)
	for i := range b.Projects {
		doc := &b.Projects[i]
		remote, err := kinds.FetchProjectBySlug(ctx, c, doc.Metadata.Slug)
		if err != nil {
			return fmt.Errorf("project %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("project %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 4: Labels (no deps)
	for i := range b.Labels {
		doc := &b.Labels[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("label %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 4.5: Skills (no FK deps; agents reference them but the
	// agent docs run later in the legacy crew-bundle path). Placed
	// after Label so the topological order matches: "foundations
	// (Project/Label/Skill) before structure (Milestone, Routine,
	// etc.) before bindings". A per-doc remote lookup so Plan can
	// emit Update on metadata drift.
	for i := range b.Skills {
		doc := &b.Skills[i]
		remote, err := kinds.LookupSkillRemoteBySlug(ctx, c, doc.Metadata.Slug)
		if err != nil {
			return fmt.Errorf("skill %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("skill %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 5: Milestones (deps: Projects)
	for i := range b.Milestones {
		doc := &b.Milestones[i]
		remote, err := kinds.LookupMilestoneRemote(ctx, c, doc)
		if err != nil {
			return fmt.Errorf("milestone %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("milestone %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 6: WorkflowTemplates
	for i := range b.WorkflowTemplates {
		doc := &b.WorkflowTemplates[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("workflow_template %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 7: FeatureFlags
	for i := range b.FeatureFlags {
		doc := &b.FeatureFlags[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("feature_flag %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 8: InstanceSettings — uses a slightly different Plan signature
	// (takes an options struct for env interpolation + replace mode).
	// The defaults below mirror ApplyUpsert + os.LookupEnv, which is
	// what every other kind already does implicitly.
	for i := range b.InstanceSettings {
		doc := &b.InstanceSettings[i]
		opts := kinds.PlanInstanceSettingsOptions{
			Replace:   pb.opts.Mode == ApplyReplace,
			EnvLookup: nil, // nil → kinds package falls back to os.LookupEnv
		}
		items, err := doc.Plan(ctx, c, nil, opts)
		if err != nil {
			return fmt.Errorf("instance_setting %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 9: Recipes (catalog install)
	for i := range b.Recipes {
		doc := &b.Recipes[i]
		remote, err := kinds.LookupRecipeRemote(ctx, c, doc)
		if err != nil {
			return fmt.Errorf("recipe %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("recipe %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 10: CrewTemplates (deploy)
	for i := range b.CrewTemplates {
		doc := &b.CrewTemplates[i]
		remote, err := kinds.LookupCrewTemplateRemote(ctx, c, doc)
		if err != nil {
			return fmt.Errorf("crew_template %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("crew_template %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 11: Connectors (catalog install)
	for i := range b.Connectors {
		doc := &b.Connectors[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("connector %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 12: Routines (deps: Crews, Agents) — also fans out into
	// schedule + webhook plan items per the RoutineDocument.Plan
	// contract, so a single document can produce up to 1+N+1 items.
	for i := range b.Routines {
		doc := &b.Routines[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("routine %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 14: RecurringIssues (deps: Projects, Labels, Crews)
	for i := range b.RecurringIssues {
		doc := &b.RecurringIssues[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("recurring_issue %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 14.1: Crews (top-level, no nested-bundle shape). Must
	// land before Agent/Integration so FK refs in agent/integration
	// docs (`crew_slug:`) can resolve. Legacy bundle Crew (with
	// nested agents/skills) is dispatched elsewhere via b.Documents.
	for i := range b.Crews {
		doc := &b.Crews[i]
		remote, err := kinds.LookupCrewRemoteBySlug(ctx, c, doc.Metadata.Slug)
		if err != nil {
			return fmt.Errorf("crew %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("crew %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 14.2: Agents (deps: Crew). Top-level kind only — agents
	// nested inside a legacy Crew bundle still flow through
	// b.Documents and the existing crew/workspace planner.
	for i := range b.Agents {
		doc := &b.Agents[i]
		remote, err := kinds.LookupAgentRemoteBySlug(ctx, c, doc.Metadata.Slug)
		if err != nil {
			return fmt.Errorf("agent %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("agent %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 14.3: Integrations (deps: Crew when spec.crew_slug set,
	// otherwise workspace-scoped). LookupIntegrationRemoteBySlug
	// takes the scope + crew_slug from the doc so a workspace-
	// integration with the same slug as a crew-scoped one doesn't
	// alias.
	for i := range b.Integrations {
		doc := &b.Integrations[i]
		scope := doc.Spec.Scope
		if scope == "" {
			if doc.Spec.CrewSlug != "" {
				scope = "crew"
			} else {
				scope = "workspace"
			}
		}
		remote, err := kinds.LookupIntegrationRemoteBySlug(ctx, c, doc.Metadata.Slug, scope, doc.Spec.CrewSlug)
		if err != nil {
			return fmt.Errorf("integration %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("integration %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 14.5: Issues (deps: Crew + optional Project / Agent / Labels).
	// Crew-scoped — drift detection matches (crew_id, title) because
	// the missions table has no slug column; the kind's
	// LookupIssueRemoteBySlug consumes the resolved title here.
	// Title fallback (spec.title || metadata.name) is inlined to keep
	// the kind's resolvedTitle helper unexported.
	for i := range b.Issues {
		doc := &b.Issues[i]
		title := doc.Spec.Title
		if title == "" {
			title = doc.Metadata.Name
		}
		remote, err := kinds.LookupIssueRemoteBySlug(ctx, c, doc.Metadata.Slug, doc.Spec.CrewSlug, title)
		if err != nil {
			return fmt.Errorf("issue %q: lookup remote: %w", doc.Metadata.Slug, err)
		}
		items, err := doc.Plan(ctx, c, remote)
		if err != nil {
			return fmt.Errorf("issue %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 15: TriageRules (deps: Projects, Labels, Crews)
	for i := range b.TriageRules {
		doc := &b.TriageRules[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("triage_rule %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 16: SavedViews (deps: Labels, Projects)
	for i := range b.SavedViews {
		doc := &b.SavedViews[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("saved_view %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	// Phase 17: Hooks (toggle existing)
	for i := range b.Hooks {
		doc := &b.Hooks[i]
		items, err := doc.Plan(ctx, c, nil)
		if err != nil {
			return fmt.Errorf("hook %q: plan: %w", doc.Metadata.Slug, err)
		}
		pb.appendKindItems(items)
	}

	return nil
}

// validateAllKinds runs kind.Validate against the merged
// WorkspaceContext for every declared document. Aggregating failures
// up front means a manifest with a typo in 3 places reports all 3 —
// no whack-a-mole.
//
// Also runs duplicate-slug detection per kind. Pre-this-addition,
// two documents of the same kind with the same metadata.slug in one
// bundle would both pass Validate(), then the second one would
// silently overwrite the first at server apply (CREATE-OR-UPDATE
// keyed on slug). Catch at validate time so the operator sees the
// typo instead of a missing entity post-apply.
func validateAllKinds(b *Bundle, wsCtx internalapi.WorkspaceContext) error {
	var errs []string
	check := func(slug string, err error) {
		if err != nil {
			errs = append(errs, fmt.Sprintf("  - %s: %s", slug, err.Error()))
		}
	}
	// checkDups appends a "duplicate <kind> slug" entry to errs for
	// any slug that appears more than once. Reports the first
	// duplicate per kind to keep the failure list focused.
	checkDups := func(kind string, slugs []string) {
		seen := make(map[string]int, len(slugs))
		for _, s := range slugs {
			seen[s]++
		}
		for s, n := range seen {
			if n > 1 {
				errs = append(errs, fmt.Sprintf("  - duplicate %s slug %q appears %d times in this bundle", kind, s, n))
			}
		}
	}
	// Per-kind slug collection — type-safe, no reflection.
	collect := func(getSlug func(i int) string, count int) []string {
		out := make([]string, count)
		for i := 0; i < count; i++ {
			out[i] = getSlug(i)
		}
		return out
	}
	for i := range b.Projects {
		check(b.Projects[i].Metadata.Slug, b.Projects[i].Validate(wsCtx))
	}
	for i := range b.Labels {
		check(b.Labels[i].Metadata.Slug, b.Labels[i].Validate(wsCtx))
	}
	for i := range b.Skills {
		check(b.Skills[i].Metadata.Slug, b.Skills[i].Validate(wsCtx))
	}
	for i := range b.Crews {
		check(b.Crews[i].Metadata.Slug, b.Crews[i].Validate(wsCtx))
	}
	for i := range b.Agents {
		check(b.Agents[i].Metadata.Slug, b.Agents[i].Validate(wsCtx))
	}
	for i := range b.Integrations {
		check(b.Integrations[i].Metadata.Slug, b.Integrations[i].Validate(wsCtx))
	}
	for i := range b.Issues {
		check(b.Issues[i].Metadata.Slug, b.Issues[i].Validate(wsCtx))
	}
	for i := range b.Milestones {
		check(b.Milestones[i].Metadata.Slug, b.Milestones[i].Validate(wsCtx))
	}
	for i := range b.WorkflowTemplates {
		check(b.WorkflowTemplates[i].Metadata.Slug, b.WorkflowTemplates[i].Validate(wsCtx))
	}
	for i := range b.TriageRules {
		check(b.TriageRules[i].Metadata.Slug, b.TriageRules[i].Validate(wsCtx))
	}
	for i := range b.RecurringIssues {
		check(b.RecurringIssues[i].Metadata.Slug, b.RecurringIssues[i].Validate(wsCtx))
	}
	for i := range b.SavedViews {
		check(b.SavedViews[i].Metadata.Slug, b.SavedViews[i].Validate(wsCtx))
	}
	for i := range b.Routines {
		check(b.Routines[i].Metadata.Slug, b.Routines[i].Validate(wsCtx))
	}
	for i := range b.FeatureFlags {
		check(b.FeatureFlags[i].Metadata.Slug, b.FeatureFlags[i].Validate(wsCtx))
	}
	for i := range b.InstanceSettings {
		check(b.InstanceSettings[i].Metadata.Slug, b.InstanceSettings[i].Validate(wsCtx))
	}
	for i := range b.Recipes {
		check(b.Recipes[i].Metadata.Slug, b.Recipes[i].Validate(wsCtx))
	}
	for i := range b.CrewTemplates {
		check(b.CrewTemplates[i].Metadata.Slug, b.CrewTemplates[i].Validate(wsCtx))
	}
	for i := range b.Connectors {
		check(b.Connectors[i].Metadata.Slug, b.Connectors[i].Validate(wsCtx))
	}
	for i := range b.Hooks {
		check(b.Hooks[i].Metadata.Slug, b.Hooks[i].Validate(wsCtx))
	}
	// Duplicate-slug detection for the new top-level kinds + the
	// other SPEC-2 surfaces. Skips legacy Documents (Crew bundles)
	// which already have their own duplicate-slug check inside
	// Bundle.Validate() (see validate.go:107).
	checkDups("Project", collect(func(i int) string { return b.Projects[i].Metadata.Slug }, len(b.Projects)))
	checkDups("Label", collect(func(i int) string { return b.Labels[i].Metadata.Slug }, len(b.Labels)))
	checkDups("Skill", collect(func(i int) string { return b.Skills[i].Metadata.Slug }, len(b.Skills)))
	checkDups("Crew", collect(func(i int) string { return b.Crews[i].Metadata.Slug }, len(b.Crews)))
	checkDups("Agent", collect(func(i int) string { return b.Agents[i].Metadata.Slug }, len(b.Agents)))
	checkDups("Integration", collect(func(i int) string { return b.Integrations[i].Metadata.Slug }, len(b.Integrations)))
	checkDups("Issue", collect(func(i int) string { return b.Issues[i].Metadata.Slug }, len(b.Issues)))
	checkDups("Milestone", collect(func(i int) string { return b.Milestones[i].Metadata.Slug }, len(b.Milestones)))
	checkDups("WorkflowTemplate", collect(func(i int) string { return b.WorkflowTemplates[i].Metadata.Slug }, len(b.WorkflowTemplates)))
	checkDups("TriageRule", collect(func(i int) string { return b.TriageRules[i].Metadata.Slug }, len(b.TriageRules)))
	checkDups("RecurringIssue", collect(func(i int) string { return b.RecurringIssues[i].Metadata.Slug }, len(b.RecurringIssues)))
	checkDups("SavedView", collect(func(i int) string { return b.SavedViews[i].Metadata.Slug }, len(b.SavedViews)))
	checkDups("Routine", collect(func(i int) string { return b.Routines[i].Metadata.Slug }, len(b.Routines)))
	checkDups("FeatureFlag", collect(func(i int) string { return b.FeatureFlags[i].Metadata.Slug }, len(b.FeatureFlags)))
	checkDups("InstanceSetting", collect(func(i int) string { return b.InstanceSettings[i].Metadata.Slug }, len(b.InstanceSettings)))
	checkDups("Recipe", collect(func(i int) string { return b.Recipes[i].Metadata.Slug }, len(b.Recipes)))
	checkDups("CrewTemplate", collect(func(i int) string { return b.CrewTemplates[i].Metadata.Slug }, len(b.CrewTemplates)))
	checkDups("Connector", collect(func(i int) string { return b.Connectors[i].Metadata.Slug }, len(b.Connectors)))
	checkDups("Hook", collect(func(i int) string { return b.Hooks[i].Metadata.Slug }, len(b.Hooks)))
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("kind validation failed:\n%s", joinLines(errs))
}

// buildKindWorkspaceContext extracts slugs from every declared kind so
// FK validators can verify "the project this milestone references is
// somewhere in the same manifest" without round-tripping to the
// server. Workspace nested crews are also surfaced as crews +
// agents because Routine / RecurringIssue / TriageRule reference
// them by slug.
func buildKindWorkspaceContext(b *Bundle) internalapi.WorkspaceContext {
	ctx := internalapi.WorkspaceContext{}

	// Declared crews + agents come from both top-level Crew documents
	// and Workspace.Spec.Crews entries.
	for i := range b.Documents {
		doc := &b.Documents[i]
		if doc.Spec == nil {
			continue
		}
		ctx.DeclaredCrews = append(ctx.DeclaredCrews, internalapi.SlugLookup{
			Slug: doc.Metadata.Slug,
			Name: doc.Metadata.Name,
		})
		for _, a := range doc.Spec.Agents {
			ctx.DeclaredAgents = append(ctx.DeclaredAgents, internalapi.SlugLookup{
				Slug: a.Slug,
				Name: a.Name,
			})
		}
	}
	for i := range b.Workspaces {
		for _, crew := range b.Workspaces[i].Spec.Crews {
			ctx.DeclaredCrews = append(ctx.DeclaredCrews, internalapi.SlugLookup{
				Slug: crew.SlugOverride,
				Name: crew.Name,
			})
			for _, a := range crew.Agents {
				ctx.DeclaredAgents = append(ctx.DeclaredAgents, internalapi.SlugLookup{
					Slug: a.Slug,
					Name: a.Name,
				})
			}
		}
	}

	// New-kind slug lookups.
	for i := range b.Projects {
		ctx.DeclaredProjects = append(ctx.DeclaredProjects, internalapi.SlugLookup{
			Slug: b.Projects[i].Metadata.Slug, Name: b.Projects[i].Metadata.Name,
		})
	}
	for i := range b.Labels {
		ctx.DeclaredLabels = append(ctx.DeclaredLabels, internalapi.SlugLookup{
			Slug: b.Labels[i].Metadata.Slug, Name: b.Labels[i].Metadata.Name,
		})
	}
	for i := range b.Milestones {
		ctx.DeclaredMilestones = append(ctx.DeclaredMilestones, internalapi.SlugLookup{
			Slug: b.Milestones[i].Metadata.Slug, Name: b.Milestones[i].Metadata.Name,
		})
	}
	for i := range b.Routines {
		ctx.DeclaredRoutines = append(ctx.DeclaredRoutines, internalapi.SlugLookup{
			Slug: b.Routines[i].Metadata.Slug, Name: b.Routines[i].Metadata.Name,
		})
	}

	return ctx
}

// appendKindItems converts a []internalapi.PlanItem into the
// manifest-package PlanItem shape, mapping the action enum and
// wrapping the Exec closure so the existing apply pipeline can run
// it. The kinds package never sees manifest.Options, so its closures
// take just (ctx, internalapi.Client); the wrapper here passes the
// already-adapted client and ignores opts.
func (pb *planBuilder) appendKindItems(items []internalapi.PlanItem) {
	for _, it := range items {
		action := mapKindAction(it.Action)
		exec := wrapKindExec(it.Exec, pb.client)
		pb.plan.Items = append(pb.plan.Items, PlanItem{
			Action:      action,
			Kind:        it.Kind,
			Description: planItemDesc(it),
			exec:        exec,
		})
	}
}

func planItemDesc(it internalapi.PlanItem) string {
	if it.Description != "" {
		return it.Description
	}
	return it.Slug
}

// mapKindAction translates internalapi.PlanAction (0=Unchanged,
// 1=Create, 2=Update, 3=Delete) into the local manifest.Action
// (0=Create, 1=Update, 2=Unchanged, 3=Delete). The two enums differ
// because the local one orders by execution priority for the plan
// sorter, while internalapi's keeps "Unchanged" as the zero value
// for easier per-kind decision logic.
func mapKindAction(a internalapi.PlanAction) Action {
	switch a {
	case internalapi.ActionCreate:
		return ActionCreate
	case internalapi.ActionUpdate:
		return ActionUpdate
	case internalapi.ActionDelete:
		return ActionDelete
	default:
		return ActionUnchanged
	}
}

// wrapKindExec hides the closure-signature mismatch between the
// kinds package (which only wants ctx + Client) and the manifest
// apply loop (which passes ctx + *Client + Options). A nil inner
// closure → nil wrapper, so ActionUnchanged items don't get a
// no-op call.
func wrapKindExec(inner func(ctx context.Context, c internalapi.Client) error, c *Client) func(ctx context.Context, _ *Client, _ Options) error {
	if inner == nil {
		return nil
	}
	adapter := newInternalClient(c)
	return func(ctx context.Context, _ *Client, _ Options) error {
		return inner(ctx, adapter)
	}
}

// joinLines is a tiny strings.Join helper that's local so this file
// doesn't pull in strings just for one call site.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for _, l := range lines[1:] {
		out += "\n" + l
	}
	return out
}
