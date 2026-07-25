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
// Credential LEASES (#1373) are NOT enforced through this fetch. An earlier
// increment claimed they were, on the grounds that the crew-scoped listing
// lease-gates its agent_credentials EXISTS clause — but that clause is one arm of
// an OR with `credentials.scope = 'WORKSPACE'`, which is the default scope for
// exactly the provider keys (API_KEY / AI_CLI_TOKEN) delivered here, so a leased
// provider key stayed listed forever. More fundamentally, boot delivery is
// credential-scoped (one crew-wide store keyed by credential id) while a lease is
// grant-scoped (per agent), so a crew-wide listing has no dimension in which to
// express "agent A's lease lapsed". Lease expiry is therefore enforced against a
// deadline delivered WITH each credential — see CredStore.ExpireLeases and the
// lease gate in Select — which also makes it fail-closed rather than dependent on
// this fetch succeeding.
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

// startCredentialReaper runs both sweeps on a ticker until ctx is cancelled:
//
//  1. Lease expiry (#1373) — local, needs no server, so it runs on EVERY tick
//     regardless of IPC configuration. Gating it on IPC (as the revocation sweep
//     is) would mean a sidecar started without an IPC config never evicts a
//     lapsed lease's plaintext from memory.
//  2. Revocation reconciliation — requires crewshipd; reapRevokedCredentials
//     guards its own preconditions and fails open on any fetch error.
//
// The loop itself only needs a credStore, so it is no longer short-circuited by a
// missing IPC config.
func (s *Server) startCredentialReaper(ctx context.Context) {
	if s == nil || s.credStore == nil {
		return
	}
	ticker := time.NewTicker(credReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticker.C:
			if dropped := s.credStore.ExpireLeases(tick); dropped > 0 {
				s.logger.Info("credential reap: dropped credentials with lapsed leases", "count", dropped)
			}
			s.reapRevokedCredentials(ctx)
		}
	}
}
