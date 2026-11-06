package api

// Skill content lock (#5045). A person who curated a skill can lock it so no
// agent, sidecar tool, consolidation or promotion path rewrites it, and the
// lifecycle sweep does not age it out. Skills are one catalog for the whole
// instance with no recorded owner, and a lock binds every workspace that uses
// the skill, so only an interactive instance administrator may lock or unlock.
// Assigning a skill to agents grants nothing here. Run tokens, internal
// (sidecar) tokens and agent identities are refused.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/skills"
)

// skillLockHuman reports whether the request carries a human credential: a
// session or CLI token that went through RequireAuth and is not a run,
// internal or agent-bound identity, nor a scope-limited chat token. Absence
// of any signal denies.
func skillLockHuman(ctx context.Context) bool {
	user := UserFromContext(ctx)
	if user == nil {
		return false
	}
	if kind := AuthKindFromContext(ctx); kind != AuthKindSession && kind != AuthKindCLIToken {
		return false
	}
	return InternalTokenWorkspaceFromContext(ctx) == "" &&
		InternalTokenCrewFromContext(ctx) == "" &&
		projectAgentFrom(ctx) == nil &&
		ctx.Value(artifactRunKey{}) == nil &&
		!limitedChatToken(ctx)
}

// Lock handles POST /api/v1/workspaces/{workspaceId}/skills/{skillId}/lock.
func (h *SkillHandler) Lock(w http.ResponseWriter, r *http.Request) { h.setLock(w, r, true) }

// Unlock handles POST /api/v1/workspaces/{workspaceId}/skills/{skillId}/unlock.
func (h *SkillHandler) Unlock(w http.ResponseWriter, r *http.Request) { h.setLock(w, r, false) }

func (h *SkillHandler) setLock(w http.ResponseWriter, r *http.Request, lock bool) {
	problem := func(status int, detail string) {
		writeProblem(w, r, status, detail)
	}
	if !skillLockHuman(r.Context()) {
		problem(http.StatusForbidden, "locking or unlocking a skill needs a signed-in person; run tokens, sidecar tokens and agent identities are refused")
		return
	}
	if !canRole(RoleFromContext(r.Context()), "manage") {
		problem(http.StatusForbidden, "Forbidden")
		return
	}
	// Workspace roles and assignments are things a workspace grants itself, so
	// neither can authorise a change to a row every workspace shares.
	if !isInstanceAdmin(r, h.db) {
		problem(http.StatusForbidden, "skills are shared across the instance; locking or unlocking one needs an instance administrator")
		return
	}
	skillID := r.PathValue("skillId")
	wsID := WorkspaceIDFromContext(r.Context())
	var slug, source string
	var was int
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, locked, source FROM skills WHERE id = ?", skillID).Scan(&slug, &was, &source)
	if errors.Is(err, sql.ErrNoRows) {
		problem(http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "skill lock lookup", err)
		return
	}
	if source == "BUNDLED" {
		problem(http.StatusForbidden, "BUNDLED skills ship with the product and are re-seeded; refusing to lock or unlock")
		return
	}
	userID := UserFromContext(r.Context()).ID
	want := 0
	if lock {
		want = 1
	}
	if was != want {
		var err error
		now := time.Now().UTC().Format(time.RFC3339)
		if lock {
			_, err = h.db.ExecContext(r.Context(),
				`UPDATE skills SET locked = 1, locked_by = ?, locked_at = ?, updated_at = ? WHERE id = ?`,
				userID, now, now, skillID)
		} else {
			_, err = h.db.ExecContext(r.Context(),
				`UPDATE skills SET locked = 0, locked_by = NULL, locked_at = NULL, updated_at = ? WHERE id = ?`,
				now, skillID)
		}
		if err != nil {
			replyInternalError(w, h.logger, "skill lock update", err)
			return
		}
		entryType, verb := journal.EntrySkillLocked, "locked"
		if !lock {
			entryType, verb = journal.EntrySkillUnlocked, "unlocked"
		}
		if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: wsID,
			Type:        entryType,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     userID,
			Summary:     "skill " + verb + ": " + slug,
			Payload:     map[string]any{"skill_id": skillID, "slug": slug},
		}); jerr != nil {
			h.logger.Warn("skill lock journal emit failed", "error", jerr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill_id": skillID, "slug": slug, "locked": lock})
}

// refuseLockedSkill answers a write refused by a content lock: it journals the
// blocked attempt and writes 423 naming the lock. It returns false when err is
// not a lock error, so callers can chain it ahead of their own error mapping.
func refuseLockedSkill(w http.ResponseWriter, r *http.Request, j journal.Emitter, wsID, crewID string, actor journal.ActorType, actorID, path string, err error) bool {
	var le *skills.LockedError
	if !errors.As(err, &le) {
		return false
	}
	emitSkillWriteBlocked(r.Context(), j, wsID, crewID, actor, actorID, le.Slug, path)
	writeProblem(w, r, http.StatusLocked, le.Error())
	return true
}

func emitSkillWriteBlocked(ctx context.Context, j journal.Emitter, wsID, crewID string, actor journal.ActorType, actorID, slug, path string) {
	if j == nil {
		return
	}
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		CrewID:      crewID,
		Type:        journal.EntrySkillWriteBlocked,
		Severity:    journal.SeverityNotice,
		ActorType:   actor,
		ActorID:     actorID,
		Summary:     "skill write blocked, skill is locked: " + slug,
		Payload:     map[string]any{"slug": slug, "path": path},
	})
}

// skillWriteActor names who attempted a blocked write for the journal.
func skillWriteActor(r *http.Request) (journal.ActorType, string) {
	if skillLockHuman(r.Context()) {
		return journal.ActorUser, UserFromContext(r.Context()).ID
	}
	return journal.ActorAgent, ""
}
