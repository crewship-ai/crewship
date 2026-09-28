package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

// A healthy authority refreshes every minute. After two missed refreshes,
// new selections fail closed; an already admitted upstream request may finish.
const credentialGrantMaxAge = 2 * time.Minute

func (c *Credential) setAgentGrants(grants map[string]string, now time.Time) {
	c.AgentGrants = make(map[string]string, len(grants))
	c.agentGrantDeadlines = make(map[string]time.Time, len(grants))
	for id, raw := range grants {
		c.AgentGrants[id] = raw
		if raw == "" {
			c.agentGrantDeadlines[id] = time.Time{}
			continue
		}
		deadline, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			deadline = leaseEpochSentinel
		}
		c.agentGrantDeadlines[id] = deadline
	}
	c.grantsValidUntil = now.Add(credentialGrantMaxAge)
}

func (cs *CredStore) needsGrantRefresh() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for i := range cs.creds {
		if cs.creds[i].AgentGrants != nil {
			return true
		}
	}
	return false
}

// RefreshAgentGrants changes only authority, never tokens or upstream hosts.
// Missing credential/agent entries are explicit denials, not crew-wide grants.
func (cs *CredStore) RefreshAgentGrants(grants map[string]map[string]string, now time.Time) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for i := range cs.creds {
		if cs.creds[i].AgentGrants != nil {
			cs.creds[i].setAgentGrants(grants[cs.creds[i].ID], now)
		}
	}
}

func (s *Server) refreshCredentialGrants(ctx context.Context) {
	if s == nil || s.credStore == nil || !s.credStore.needsGrantRefresh() || s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.CrewID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	endpoint := s.ipc.BaseURL + "/api/v1/internal/credential-grants?workspace_id=" + url.QueryEscape(s.ipc.WorkspaceID) + "&crew_id=" + url.QueryEscape(s.ipc.CrewID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		s.logGrantRefreshFailure("request_build_failed")
		return
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)
	resp, err := ipcClient.Do(req)
	if err != nil {
		s.logGrantRefreshFailure("authority_unreachable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.logGrantRefreshFailure("authority_http_error")
		return
	}
	var snapshot struct {
		Version int                          `json:"version"`
		Grants  map[string]map[string]string `json:"grants"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&snapshot); err != nil || snapshot.Version != 1 || snapshot.Grants == nil {
		s.logGrantRefreshFailure("invalid_snapshot")
		return
	}
	s.credStore.RefreshAgentGrants(snapshot.Grants, time.Now())
	if s.grantRefreshFailed.Swap(false) && s.logger != nil {
		s.logger.Info("credential grant authority recovered")
	}
}

// Log transitions only, without response bodies, tokens or URL-bearing errors.
func (s *Server) logGrantRefreshFailure(reason string) {
	if !s.grantRefreshFailed.Swap(true) && s.logger != nil {
		s.logger.Warn("credential grant authority refresh failed", "reason", reason)
	}
}
