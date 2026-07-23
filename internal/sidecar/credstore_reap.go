package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

// credReapInterval is how often the sidecar reconciles its in-memory CredStore
// against crewshipd's live credential set. The store is a boot-time snapshot
// with no way to see a later revocation; without this a REVOKED provider key
// keeps being served for the whole agent-process life.
const credReapInterval = 60 * time.Second

// reapRevokedCredentials fetches the metadata-only credential list from
// crewshipd (which excludes REVOKED/deleted rows) and drops any CredStore entry
// that is no longer listed. It never adds or replaces tokens — the sidecar has
// no plaintext supply line after boot (the boot stdin blob is the only source;
// the live endpoint is metadata-only for non-loopback callers) — so a valid
// key's in-memory plaintext is retained. On ANY fetch/parse error it does
// nothing (fail toward availability: a transient crewshipd blip must not nuke
// working keys; the revoked key is simply reaped on the next good tick).
//
// #1373: the crew-scoped listing is also LEASE-gated on the server — a grant
// whose agent_credentials.expires_at has lapsed is omitted from the response
// exactly like a revoked one. So this same reaper is what evicts an expired
// lease: a leased provider key delivered at boot (while its lease was valid) is
// dropped from the in-memory store within one interval of its TTL lapsing, and
// the container stops being served it. No extra client-side expiry logic is
// needed — the source-of-truth query returns only non-expired ACTIVE creds.
func (s *Server) reapRevokedCredentials(ctx context.Context) {
	if s == nil || s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.WorkspaceID == "" {
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// #1031: scope to this crew, matching the agent-facing listing. The
	// CredStore's boot creds are this crew's own, so the crew-scoped live set
	// is a superset of what's in the store — a peer crew's credential is never
	// in `keep`, and no valid own-crew credential is falsely reaped. A
	// crew-less sidecar omits crew_id and keeps the workspace-wide view.
	endpoint := s.ipc.BaseURL + "/api/v1/internal/credentials?workspace_id=" + url.QueryEscape(s.ipc.WorkspaceID)
	if s.ipc.CrewID != "" {
		endpoint += "&crew_id=" + url.QueryEscape(s.ipc.CrewID)
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		s.logger.Warn("credential reap: build request", "error", err)
		return
	}
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Warn("credential reap: fetch failed, keeping current creds", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Non-200 → don't trust the (possibly empty) body; keep current creds.
		s.logger.Warn("credential reap: non-200 from crewshipd, keeping current creds", "status", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		s.logger.Warn("credential reap: read body", "error", err)
		return
	}
	// Metadata shape (values withheld for the non-loopback sidecar): we only
	// need the live IDs.
	var live []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &live); err != nil {
		s.logger.Warn("credential reap: decode failed, keeping current creds", "error", err)
		return
	}

	keep := make(map[string]struct{}, len(live))
	for _, c := range live {
		if c.ID != "" {
			keep[c.ID] = struct{}{}
		}
	}
	if removed := s.credStore.Reap(keep); removed > 0 {
		s.logger.Info("credential reap: dropped revoked/removed credentials", "count", removed)
	}
}

// startCredentialReaper runs reapRevokedCredentials on a ticker until ctx is
// cancelled. Guarded so it's a no-op without an IPC config (tests, standalone).
func (s *Server) startCredentialReaper(ctx context.Context) {
	if s == nil || s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.WorkspaceID == "" {
		return
	}
	ticker := time.NewTicker(credReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapRevokedCredentials(ctx)
		}
	}
}
