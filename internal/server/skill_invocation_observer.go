// PR #7 skill-invocation telemetry: trusted-tier consumer of the
// orchestrator's SkillInvocationObserver tap. Where the behavior monitor
// (post_tool_call_adapter.go) samples tool calls for an LLM safety check,
// this observer records *which assigned skill an agent actually invoked*
// so the F4.1 skill-review sweep can answer "is this skill in use?" from
// real telemetry rather than guesswork.
//
// Trust tier: this observer holds the *sql.DB + journal writer (the
// orchestrator does not — it only knows the narrow SkillInvocation
// struct). The match→record→denormalise→emit path runs entirely server-
// side. The observer is invoked from a bounded goroutine on the
// orchestrator hot path, so every method is best-effort: errors are
// logged, never returned, and a non-skill tool call is a silent no-op.
//
// Flow per Observe:
//  1. resolve the agent's enabled skill slugs (cached per agent_id);
//  2. match tool_name=="Skill" + input slug, or tool_name against an
//     assigned slug, to a single skill_id;
//  3. in one txn: INSERT INTO skill_invocations + bump
//     skills.usage_count/last_used_at (+error_count when exit_code != 0);
//  4. emit a skill.invoked journal entry.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"

	"github.com/google/uuid"
)

// skillInvocationObserver implements orchestrator.SkillInvocationObserver.
type skillInvocationObserver struct {
	logger *slog.Logger
	db     *sql.DB
	journ  journal.Emitter

	// slugCache memoises the per-agent enabled-skill slug→id map so the
	// hot path doesn't re-run the agent_skills JOIN on every tool call.
	// Keyed by agent_id. Entries carry a timestamp and expire after
	// skillSlugCacheTTL so enable/disable/reassignment changes in
	// agent_skills are reflected within the TTL window without paying a
	// JOIN per tool call — a bounded staleness instead of process-lifetime.
	mu        sync.Mutex
	slugCache map[string]slugCacheEntry // agent_id → (slugs + fetch time)

	// now is an injectable clock for the cache TTL (nil → time.Now).
	now func() time.Time
}

// skillSlugCacheTTL bounds how long a cached per-agent slug map is trusted
// before a fresh agent_skills lookup. Short enough that an operator's
// reassignment takes effect promptly, long enough to keep the hot path off
// the DB under a tool storm.
const skillSlugCacheTTL = 60 * time.Second

type slugCacheEntry struct {
	slugs map[string]string
	at    time.Time
}

func newSkillInvocationObserver(logger *slog.Logger, db *sql.DB, j journal.Emitter) *skillInvocationObserver {
	return &skillInvocationObserver{
		logger:    logger,
		db:        db,
		journ:     j,
		slugCache: make(map[string]slugCacheEntry),
	}
}

func (o *skillInvocationObserver) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// Observe records a skill invocation when the tool call maps to one of
// the agent's assigned skills. Best-effort: any error short-circuits
// with a log line, never a panic or returned error.
func (o *skillInvocationObserver) Observe(obs orchestrator.SkillInvocation) {
	if o == nil || o.db == nil {
		return
	}
	if obs.WorkspaceID == "" || obs.AgentID == "" || obs.ToolName == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	slugs, err := o.assignedSlugs(ctx, obs.AgentID)
	if err != nil {
		o.logger.Debug("skill_invocation: resolve assigned slugs",
			"agent_id", obs.AgentID, "error", err)
		return
	}
	if len(slugs) == 0 {
		return // agent has no enabled skills; nothing can match
	}

	slug := matchSkillSlug(obs.ToolName, obs.Payload)
	if slug == "" {
		return
	}
	skillID, ok := slugs[slug]
	if !ok {
		return // tool call doesn't correspond to an assigned skill
	}

	exitCode := payloadExitCode(obs.Payload)
	durationMS := payloadDurationMS(obs.Payload)
	usageCount, err := o.record(ctx, obs, skillID, slug, exitCode, durationMS)
	if err != nil {
		o.logger.Warn("skill_invocation: record failed",
			"agent_id", obs.AgentID, "skill_id", skillID, "error", err)
		return
	}

	o.emit(ctx, obs, skillID, slug, exitCode, usageCount)
}

// assignedSlugs returns the agent's enabled skill slug→id map, caching
// the result per agent_id.
func (o *skillInvocationObserver) assignedSlugs(ctx context.Context, agentID string) (map[string]string, error) {
	now := o.clock()

	o.mu.Lock()
	if e, ok := o.slugCache[agentID]; ok && now.Sub(e.at) < skillSlugCacheTTL {
		o.mu.Unlock()
		return e.slugs, nil
	}
	o.mu.Unlock()

	rows, err := o.db.QueryContext(ctx, `
		SELECT s.slug, s.id
		  FROM agent_skills a
		  JOIN skills s ON s.id = a.skill_id
		 WHERE a.agent_id = ? AND a.enabled = 1`, agentID)
	if err != nil {
		return nil, fmt.Errorf("query assigned skills for agent %s: %w", agentID, err)
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var slug, id string
		if err := rows.Scan(&slug, &id); err != nil {
			return nil, fmt.Errorf("scan assigned skill row: %w", err)
		}
		if slug != "" {
			m[slug] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assigned skills: %w", err)
	}

	o.mu.Lock()
	o.slugCache[agentID] = slugCacheEntry{slugs: m, at: now}
	o.mu.Unlock()
	return m, nil
}

// record writes the skill_invocations audit row and denormalises the
// skills lifecycle counters in a single transaction, returning the
// post-increment usage_count for the journal payload.
func (o *skillInvocationObserver) record(
	ctx context.Context,
	obs orchestrator.SkillInvocation,
	skillID, slug string,
	exitCode, durationMS int,
) (int, error) {
	payloadJSON := marshalInvocationPayload(obs.Payload, slug)

	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin skill_invocations tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO skill_invocations
			(id, skill_id, agent_id, workspace_id, invoked_at, duration_ms, exit_code, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), skillID, obs.AgentID, obs.WorkspaceID,
		now, durationMS, exitCode, payloadJSON); err != nil {
		return 0, fmt.Errorf("insert skill_invocations (skill %s): %w", skillID, err)
	}

	errInc := 0
	if exitCode != 0 {
		errInc = 1
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skills
		   SET usage_count = usage_count + 1,
		       error_count = error_count + ?,
		       last_used_at = ?
		 WHERE id = ?`, errInc, now, skillID); err != nil {
		return 0, fmt.Errorf("update skills counters (skill %s): %w", skillID, err)
	}

	var usageCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT usage_count FROM skills WHERE id = ?`, skillID).Scan(&usageCount); err != nil {
		return 0, fmt.Errorf("read usage_count (skill %s): %w", skillID, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit skill_invocations tx: %w", err)
	}
	return usageCount, nil
}

func (o *skillInvocationObserver) emit(
	ctx context.Context,
	obs orchestrator.SkillInvocation,
	skillID, slug string,
	exitCode, usageCount int,
) {
	if o.journ == nil {
		return
	}
	sev := journal.SeverityInfo
	if exitCode != 0 {
		sev = journal.SeverityWarn
	}
	_, _ = o.journ.Emit(ctx, journal.Entry{
		WorkspaceID: obs.WorkspaceID,
		CrewID:      obs.CrewID,
		AgentID:     obs.AgentID,
		MissionID:   obs.MissionID,
		Type:        journal.EntrySkillInvoked,
		Severity:    sev,
		ActorType:   journal.ActorAgent,
		ActorID:     obs.AgentID,
		Summary:     "agent invoked skill " + slug,
		Payload: map[string]any{
			"skill_id":    skillID,
			"skill_slug":  slug,
			"agent_id":    obs.AgentID,
			"tool_name":   obs.ToolName,
			"exit_code":   exitCode,
			"usage_count": usageCount,
		},
	})
}

// matchSkillSlug resolves a tool call to a candidate skill slug. A
// "Skill" tool carries the target slug in its input (under skill /
// command / name / slug); any other tool name is treated as a direct
// slug candidate (CLI-style skills invoked as their own tool). Returns
// "" when no slug can be derived.
func matchSkillSlug(toolName string, payload map[string]any) string {
	if toolName == "Skill" {
		in := payloadInput(payload)
		for _, key := range []string{"skill", "command", "name", "slug"} {
			if v, ok := in[key].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	return toolName
}

// payloadInput returns the tool input map from the observation payload,
// or an empty map when absent / malformed.
func payloadInput(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	if in, ok := payload["input"].(map[string]any); ok {
		return in
	}
	return map[string]any{}
}

// payloadExitCode extracts an optional exit_code from the payload. JSON
// numbers decode to float64; ints are accepted too for direct callers.
func payloadExitCode(payload map[string]any) int {
	return payloadInt(payload, "exit_code")
}

func payloadDurationMS(payload map[string]any) int {
	return payloadInt(payload, "duration_ms")
}

func payloadInt(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	switch v := payload[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// marshalInvocationPayload bounds + serialises the tool input for the
// skill_invocations.payload_json column. The slug is always included so
// the F4.1 evaluator can read it without re-deriving from tool_name.
func marshalInvocationPayload(payload map[string]any, slug string) string {
	out := map[string]any{"skill_slug": slug}
	if in := payloadInput(payload); len(in) > 0 {
		out["input"] = in
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Keep the resolved slug for downstream attribution even when the
		// full input can't be marshalled (e.g. an unserialisable value).
		if sb, e2 := json.Marshal(map[string]any{"skill_slug": slug}); e2 == nil {
			return string(sb)
		}
		return `{"skill_slug":""}`
	}
	const cap = 4096
	if len(b) > cap {
		// Drop the input on overflow rather than store a truncated
		// (invalid) JSON blob — the slug alone is the load-bearing field.
		b, _ = json.Marshal(map[string]any{"skill_slug": slug, "truncated": true})
	}
	return string(b)
}
