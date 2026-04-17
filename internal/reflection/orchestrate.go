package reflection

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quartermaster"
)

// Reflect runs a full role-based reflection pass:
//
//  1. Spawn one Critiquer call per persona in parallel. If any fails,
//     the group's context is cancelled and the first error is returned.
//     Partial results are discarded because a missing reviewer changes
//     the balance of the synthesis — an incomplete panel is worse than
//     no panel.
//
//  2. Emit a SummaryGenerated journal entry per critique. The payload
//     carries persona, issues_count, and severity so a downstream viewer
//     can render a compact reviewer strip without re-reading the raw
//     text.
//
//  3. Call Synthesize with an ensemble-backed judge. Bias mitigation
//     (rubric shuffle, ensemble median) lives in quartermaster and is
//     not re-implemented here.
//
//  4. Emit a final SummaryGenerated entry tagged with
//     refs["reflection"]=true so the synthesis is discoverable from the
//     journal as a distinct artefact.
//
// scope carries the workspace/crew/agent/mission identifiers that every
// journal entry needs. If critiquer is nil Reflect returns a
// programming-error; same for judge and emitter.
func Reflect(
	ctx context.Context,
	req ReflectionRequest,
	critiquer Critiquer,
	judge quartermaster.JudgeInterface,
	j journal.Emitter,
	scope Scope,
) (ReflectionResult, error) {
	if critiquer == nil {
		return ReflectionResult{}, fmt.Errorf("reflection: Reflect requires a critiquer")
	}
	if judge == nil {
		return ReflectionResult{}, fmt.Errorf("reflection: Reflect requires a judge")
	}
	if j == nil {
		return ReflectionResult{}, fmt.Errorf("reflection: Reflect requires an emitter")
	}
	if scope.WorkspaceID == "" {
		return ReflectionResult{}, fmt.Errorf("reflection: scope.WorkspaceID required")
	}
	if req.Subject == "" {
		return ReflectionResult{}, fmt.Errorf("reflection: request.Subject required")
	}

	personas := req.Personas
	if len(personas) == 0 {
		personas = AllPersonas()
	}

	// Fan out critiques in parallel. errgroup.WithContext cancels every
	// in-flight goroutine on the first error, so we don't pay for
	// critiques whose result will be discarded.
	critiques := make([]Critique, len(personas))
	g, gctx := errgroup.WithContext(ctx)
	for i, p := range personas {
		i, p := i, p
		g.Go(func() error {
			c, err := critiquer.Critique(gctx, p, req.Subject, req.Context)
			if err != nil {
				return fmt.Errorf("reflection: critique %s: %w", p, err)
			}
			// Ensure Persona is set even if the Critiquer forgot.
			if c.Persona == "" {
				c.Persona = p
			}
			critiques[i] = c
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return ReflectionResult{}, err
	}

	// Per-critique journal entries. Emit failures here are non-fatal —
	// losing observability is preferable to losing the synthesis result.
	for _, c := range critiques {
		entry := journal.Entry{
			WorkspaceID: scope.WorkspaceID,
			CrewID:      scope.CrewID,
			AgentID:     scope.AgentID,
			MissionID:   scope.MissionID,
			Type:        journal.EntrySummaryGenerated,
			ActorType:   journal.ActorOrchestrator,
			ActorID:     "reflection",
			Severity:    journal.SeverityInfo,
			Summary:     fmt.Sprintf("%s reviewed subject: %d issue(s), severity=%s", c.Persona, len(c.Issues), c.Severity),
			Payload: map[string]any{
				"persona":      string(c.Persona),
				"issues_count": len(c.Issues),
				"severity":     string(c.Severity),
			},
			Refs: map[string]any{
				"reflection": true,
				"stage":      "critique",
			},
		}
		if _, err := j.Emit(ctx, entry); err != nil {
			// Swallow: journal outage must not corrupt the reflection
			// result. The caller's monitoring for journal health is
			// responsible for surfacing the outage.
			_ = err
		}
	}

	synth, verdict, err := Synthesize(ctx, judge, critiques, req.Subject)
	if err != nil {
		return ReflectionResult{}, err
	}

	// Final synthesis entry. refs["reflection"]=true makes this easy to
	// pluck out of a mission's journal without knowing its entry ID.
	synthEntry := journal.Entry{
		WorkspaceID: scope.WorkspaceID,
		CrewID:      scope.CrewID,
		AgentID:     scope.AgentID,
		MissionID:   scope.MissionID,
		Type:        journal.EntrySummaryGenerated,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     "reflection",
		Severity:    journal.SeverityInfo,
		Summary:     fmt.Sprintf("reflection synthesis: %d revisions, %d rejections, conf=%.2f", len(synth.Revise), len(synth.Reject), synth.Confidence),
		Payload: map[string]any{
			"revise_count":      len(synth.Revise),
			"reject_count":      len(synth.Reject),
			"confidence":        synth.Confidence,
			"judge_score":       verdict.Score,
			"judge_confidence":  verdict.Confidence,
			"judge_escalate":    verdict.HumanEscalate,
			"persona_count":     len(critiques),
		},
		Refs: map[string]any{
			"reflection": true,
			"stage":      "synthesis",
		},
	}
	if _, err := j.Emit(ctx, synthEntry); err != nil {
		_ = err
	}

	return ReflectionResult{
		Critiques:    critiques,
		Synthesis:    synth,
		JudgeVerdict: verdict,
	}, nil
}
