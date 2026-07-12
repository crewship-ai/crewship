package api

// SSRF-safe OLLAMA model discovery against a workspace-configured ENDPOINT_URL
// credential (#988). The `/api/v1/models?provider=OLLAMA` discovery and the
// set-time model validation in agents_update.go both funnel through
// ModelsHandler.resolveModels; for OLLAMA they should list against the
// workspace's own endpoint so a typo'd ollama/* model is caught at set time.
//
// The security constraint: this dial runs in the DAEMON (host network), not the
// sandboxed sidecar. Wiring it to the tenant-controlled ENDPOINT_URL via a
// plain http client would let any authenticated member make the daemon dial an
// arbitrary internal/LAN/loopback/metadata host — a blind SSRF + reachability
// oracle. So the discovery dial is guarded: after DNS resolution the concrete
// IP is checked with httpsafe.IsBlockedIPForEndpoint, gated on the INSTANCE cap
// CREWSHIP_ALLOW_PRIVATE_ENDPOINTS (discovery has no per-crew context). Metadata
// / link-local is blocked unconditionally; RFC1918/loopback only when the
// operator enabled private endpoints instance-wide.
//
// The server-global KEEPER_OLLAMA_URL path is intentionally NOT guarded — it is
// operator-configured (trusted) and is typically loopback, which the guard
// would otherwise block.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/llm"
)

// instanceAllowsPrivateEndpoints reports whether the operator enabled private-
// network model endpoints instance-wide (#974 S5 / #988). Mirrors the
// orchestrator-side helper; kept local to avoid an api→orchestrator import.
func instanceAllowsPrivateEndpoints() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CREWSHIP_ALLOW_PRIVATE_ENDPOINTS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ollamaDiscoveryClient returns an http.Client whose dialer refuses a resolved
// IP that IsBlockedIPForEndpoint rejects under the instance cap — so a tenant
// endpoint resolving to metadata/link-local (always) or RFC1918/loopback (unless
// the cap is on) cannot be reached from the daemon. No redirect following.
func ollamaDiscoveryClient() *http.Client {
	allowPrivate := instanceAllowsPrivateEndpoints()
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
				Control: func(_, address string, _ syscall.RawConn) error {
					host, _, err := net.SplitHostPort(address)
					if err != nil {
						return err
					}
					ip := net.ParseIP(host)
					if ip == nil {
						return nil // resolver produces concrete IPs on real dials
					}
					if httpsafe.IsBlockedIPForEndpoint(ip, allowPrivate) {
						return fmt.Errorf("ollama discovery endpoint resolves to a blocked address (%s) — set CREWSHIP_ALLOW_PRIVATE_ENDPOINTS to reach a private endpoint", ip)
					}
					return nil
				},
			}).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// defaultWorkspaceOllamaLister builds an SSRF-guarded ModelLister for a tenant
// endpoint base URL. Returns ok=false for an empty URL so the caller falls back
// to the server-global path.
func defaultWorkspaceOllamaLister(baseURL string) (llm.ModelLister, bool) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, false
	}
	return llm.NewOllamaWithClient(baseURL, "", ollamaDiscoveryClient()), true
}
