package sidecar

// #1232 (the #1160 residual question): does a restricted-mode crew with a
// NON-EMPTY domain allowlist restart its sidecar on every exec?
//
// sidecarNeedsRestart (internal/orchestrator/exec_sidecar.go) skips the
// restart only when the hash the running sidecar reports on /health equals
// orchestrator.DomainsHash(desiredDomains). The two hashes are computed by
// two INDEPENDENT implementations in two packages that cannot share code
// (sidecar imports orchestrator, so the reverse import would cycle), fed
// through a JSON wire format (startSidecar's stdin payload →
// cmd/crewship-sidecar/main.go → NewServer). If ANY hop diverges — hash
// derivation, JSON tags, NewServer's policy-domain selection, /health's
// field name — the hashes never match for a non-empty allowlist and every
// exec into the crew silently degrades back to a guaranteed kill+relaunch
// of a healthy sidecar (the exact behaviour #1160/#1214 removed). The
// empty-allowlist case matching proves nothing: two different derivations
// trivially agree on the empty set.
//
// These tests pin the full round trip with the REAL components on both
// sides: the policy is marshalled with the orchestrator's wire type,
// parsed the way main.go parses stdin, served by a real NewServer, and the
// /health response is compared against orchestrator.DomainsHash over the
// same desired set RunAgent computes (req.AllowedDomains — no MCP servers,
// no local-model endpoint). This test lives in internal/sidecar because
// only this side of the boundary can import both packages.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// sidecarStdinEnvelope mirrors the network-policy part of the JSON envelope
// startSidecar pipes to the sidecar binary (exec_sidecar.go sidecarInput)
// and cmd/crewship-sidecar/main.go's sidecarInput on the reading side —
// same "network_policy" key both sides use.
type sidecarStdinEnvelope struct {
	NetworkPolicy *NetworkPolicyConfig `json:"network_policy,omitempty"`
}

// healthReportedState round-trips a network policy through the wire format
// exactly as production does — orchestrator wire type → JSON →
// NetworkPolicyConfig (main.go's parse + empty-mode default) → NewServer →
// GET /health — and returns the network_mode and domains_hash the running
// sidecar would report to checkSidecar.
func healthReportedState(t *testing.T, policy *orchestrator.SidecarNetworkPolicy) (mode, hash string) {
	t.Helper()

	// Orchestrator side: startSidecar marshals the policy under the
	// "network_policy" key of the stdin JSON envelope.
	wire, err := json.Marshal(struct {
		NetworkPolicy *orchestrator.SidecarNetworkPolicy `json:"network_policy,omitempty"`
	}{policy})
	if err != nil {
		t.Fatalf("marshal orchestrator network policy: %v", err)
	}

	// Sidecar side: main.go unmarshals the envelope and defaults an empty
	// mode to "free" before handing it to NewServer.
	var input sidecarStdinEnvelope
	if err := json.Unmarshal(wire, &input); err != nil {
		t.Fatalf("unmarshal sidecar stdin envelope: %v", err)
	}
	if input.NetworkPolicy != nil && input.NetworkPolicy.Mode == "" {
		input.NetworkPolicy.Mode = "free"
	}

	srv := NewServer(ServerConfig{
		Addr:          "127.0.0.1:0",
		Logger:        covLogger(),
		NetworkPolicy: input.NetworkPolicy,
	})

	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()
	srv.proxy.ServeHTTP(w, req)

	var resp struct {
		NetworkMode string `json:"network_mode"`
		DomainsHash string `json:"domains_hash"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal health response: %v; body=%s", err, w.Body.String())
	}
	return resp.NetworkMode, resp.DomainsHash
}

// TestHealthDomainsHashParity_NonEmptyAllowlist is the #1232 question
// itself: a restricted-mode crew with a non-empty allowlist and no MCP
// servers must report a /health domains_hash equal to the orchestrator's
// DomainsHash over the same desired set, so sidecarNeedsRestart says
// "reuse" on the next exec instead of restarting every time.
func TestHealthDomainsHashParity_NonEmptyAllowlist(t *testing.T) {
	t.Parallel()
	// Mixed case + a duplicate: both derivations must normalize identically,
	// not just agree on already-canonical input.
	domains := []string{"api.github.com", "Registry.NPMJS.org", "docs.crewship.ai", "api.github.com"}

	mode, hash := healthReportedState(t, &orchestrator.SidecarNetworkPolicy{
		Mode:           "restricted",
		AllowedDomains: domains,
	})

	if mode != "restricted" {
		t.Fatalf("running sidecar reports network_mode %q, want %q", mode, "restricted")
	}
	if hash == "" {
		// An empty reported hash trips sidecarNeedsRestart's pre-#1160
		// fail-toward-restart branch — a freshly configured sidecar must
		// never look like a legacy one.
		t.Fatal("running sidecar reports an EMPTY domains_hash for a restricted non-empty allowlist — every exec would fail toward restart")
	}
	if want := orchestrator.DomainsHash(domains); hash != want {
		t.Errorf("hash divergence: sidecar /health reports %q, orchestrator computes %q for the same allowlist — every exec into this crew would restart the sidecar (#1160 residual, #1232)", hash, want)
	}
}

// TestHealthDomainsHashParity_EmptyAllowlist pins the empty-set case (the
// one dev2 observed reusing correctly): the sidecar must report the hash of
// the EMPTY policy set — a non-empty string that matches the orchestrator's
// hash of an empty desired set, distinct from the legacy "" sentinel.
func TestHealthDomainsHashParity_EmptyAllowlist(t *testing.T) {
	t.Parallel()
	mode, hash := healthReportedState(t, &orchestrator.SidecarNetworkPolicy{
		Mode: "restricted",
	})

	if mode != "restricted" {
		t.Fatalf("running sidecar reports network_mode %q, want %q", mode, "restricted")
	}
	if hash == "" {
		t.Fatal("running sidecar reports an EMPTY domains_hash for a restricted empty allowlist — indistinguishable from a pre-#1160 sidecar, every exec would restart")
	}
	if want := orchestrator.DomainsHash(nil); hash != want {
		t.Errorf("hash divergence on the empty set: sidecar reports %q, orchestrator computes %q", hash, want)
	}
}

// TestHealthDomainsHashParity_ChangedAllowlistDiverges is the sanity
// counterpart: when the allowlist genuinely changes, the reported hash must
// NOT match the new desired set's hash, or a real policy change would be
// silently skipped.
func TestHealthDomainsHashParity_ChangedAllowlistDiverges(t *testing.T) {
	t.Parallel()
	_, hash := healthReportedState(t, &orchestrator.SidecarNetworkPolicy{
		Mode:           "restricted",
		AllowedDomains: []string{"api.github.com"},
	})

	changed := []string{"api.github.com", "evil.example.com"}
	if want := orchestrator.DomainsHash(changed); hash == want {
		t.Errorf("hash %q unexpectedly matches a DIFFERENT desired allowlist — a genuine policy change would never trigger a restart", hash)
	}
}
