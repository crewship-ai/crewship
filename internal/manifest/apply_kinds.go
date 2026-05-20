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
func validateAllKinds(b *Bundle, wsCtx internalapi.WorkspaceContext) error {
	var errs []string
	check := func(slug string, err error) {
		if err != nil {
			errs = append(errs, fmt.Sprintf("  - %s: %s", slug, err.Error()))
		}
	}
	for i := range b.Projects {
		check(b.Projects[i].Metadata.Slug, b.Projects[i].Validate(wsCtx))
	}
	for i := range b.Labels {
		check(b.Labels[i].Metadata.Slug, b.Labels[i].Validate(wsCtx))
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
