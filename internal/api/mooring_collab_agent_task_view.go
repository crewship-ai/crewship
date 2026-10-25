package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// A task view is display data, never an admission or an agent credential.
// Standing document grants and queued work cannot create one.
type collaborationAgentTaskView struct {
	WorkID    string    `json:"work_id"`
	RunID     string    `json:"run_id"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at"`
	CanRead   bool      `json:"can_read"`
	CanWrite  bool      `json:"can_write"`
}

// currentTaskViews projects only the canonical in-memory attached registry.
// The caller must hold the SQL writer and the request's original ingress lease
// through response, and separately authorize the current human's chat audience
// and Page read before disclosing even a document's name. This method grants
// nothing, does not renew a task and cannot reconstruct consent from SQL.
// An uncertain or revoked binding returns no usable projection.
func (s *collaborationAgentMandateStore) currentTaskViews(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, chatID string) ([]collaborationAgentTaskView, error) {
	if s == nil || tx == nil || chatID == "" {
		return nil, errCollaborationAgentAccess
	}
	s.mu.Lock()
	boot, tick, err := s.now()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.pruneLocked(boot, tick)
	entries := make(map[string]collaborationAgentMandateEntry)
	for ref, entry := range s.attached {
		if entry.binding.task.key == key && entry.binding.attempt.chatID == chatID {
			entries[ref] = entry
		}
	}
	s.mu.Unlock()

	views := make([]collaborationAgentTaskView, 0, len(entries))
	for _, entry := range entries {
		a, task := entry.binding.attempt, entry.binding.task
		release, err := s.fence.acquireOriginal(task.ingress)
		if err != nil {
			return nil, err
		}
		release() // The caller's full-operation ingress lease remains held.
		expected := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(s.pages.artifactMasterToken, a.workspaceID, a.crewID), a.workspaceID, a.agentID, a.runID)
		if expected == "" || sha256.Sum256([]byte(expected)) != entry.binding.credentialSHA256 {
			return nil, errCollaborationAgentAccess
		}
		current, leaseExpiry, err := loadCollaborationAgentAttempt(ctx, tx, a.runID)
		if err != nil || current != a {
			return nil, errCollaborationAgentAccess
		}
		principalExpiry, err := collaborationAgentCapturedRunHorizon(ctx, tx, task, a)
		if err != nil {
			return nil, err
		}
		page, currentPrincipalExpiry, err := s.checkTaskAuthority(ctx, tx, task, a)
		if err != nil || page.permissionVersion != task.permissionVersion {
			return nil, errCollaborationAgentAccess
		}
		horizon := collaborationAgentEarlier(page.credentialExpiry, entry.binding.expiresAt)
		horizon = collaborationAgentEarlier(horizon, leaseExpiry)
		horizon = collaborationAgentEarlier(horizon, principalExpiry)
		horizon = collaborationAgentEarlier(horizon, currentPrincipalExpiry)
		write, valid := collaborationAgentIntent(task.intent)
		if !valid {
			return nil, errCollaborationAgentAccess
		}
		views = append(views, collaborationAgentTaskView{WorkID: a.workID, RunID: a.runID, Scope: task.intent, ExpiresAt: horizon, CanRead: true, CanWrite: write})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	bootNow, tickNow, err := s.now()
	if err != nil || bootNow != boot {
		return nil, errCollaborationAgentAccess
	}
	now := time.Now()
	for ref, original := range entries {
		entry, ok := s.attached[ref]
		if !ok || entry != original || entry.boot != bootNow || tickNow >= entry.expiresTick {
			return nil, errCollaborationAgentAccess
		}
		for i := range views {
			if views[i].RunID == entry.binding.attempt.runID {
				views[i].ExpiresAt = collaborationAgentEarlier(views[i].ExpiresAt, now.Add(time.Duration(entry.expiresTick-tickNow)))
				if !now.Before(views[i].ExpiresAt) {
					return nil, errCollaborationAgentAccess
				}
			}
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].RunID < views[j].RunID })
	return views, nil
}
