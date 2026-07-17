package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/llm"
)

// GovModelResolver resolves a workspace's vault-backed governance model into a
// concrete, middleware-wrapped llm.Provider at request time (M2a, #1001). It is
// the production wiring that makes the gov-model setting LIVE: the access
// gatekeeper injects Resolve via gatekeeper.WithGovModelResolver.
//
// Revoke-safety (§4.4): the underlying governance.ResolveGovModel degrades a
// missing/revoked/undecryptable credential to the default OLLAMA judge and marks
// the result Degraded; Resolve then surfaces a WARN (log + journal) and returns
// the working fallback — never a nil/broken provider for a configured workspace.
// An UNconfigured workspace returns (nil, "") so the gatekeeper keeps its
// construction-time default.
type GovModelResolver struct {
	db      *sql.DB
	journal journal.Emitter
	logger  *slog.Logger
	lookup  governance.CredentialLookup
	dflt    governance.OllamaDefault
	ssrf    *http.Client

	mu     sync.Mutex
	cache  map[string]govModelCacheEntry // fingerprint -> wrapped provider (hot-path build cache)
	warned map[string]string             // workspaceID -> last degrade reason (WARN dedup)
}

// govModelCacheEntry pairs a built provider with the exact API key it was
// built from. The key is compared directly on lookup instead of being hashed
// into the cache key: secret material never flows through a fast hash (CodeQL
// go/weak-sensitive-data-hashing, #954 alert 667), a rotated key can never
// collide back onto a stale provider, and rotation replaces the entry in
// place rather than leaking one cache slot per historical key.
type govModelCacheEntry struct {
	apiKey   string
	provider llm.Provider
}

// NewGovModelResolver builds the resolver. ollamaURL/ollamaModel are the server
// default judge (cfg.Keeper.OllamaURL / cfg.Keeper.Model) used as the §4.4
// degrade fallback. journal may be nil (degrade WARN then logs only).
func NewGovModelResolver(db *sql.DB, j journal.Emitter, logger *slog.Logger, ollamaURL, ollamaModel string) *GovModelResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &GovModelResolver{
		db:      db,
		journal: j,
		logger:  logger,
		lookup:  newGovModelCredentialLookup(db),
		dflt:    governance.OllamaDefault{URL: ollamaURL, Model: ollamaModel},
		ssrf:    govModelSSRFClient(),
		cache:   map[string]govModelCacheEntry{},
		warned:  map[string]string{},
	}
}

// Resolve implements gatekeeper.GovModelResolver.
func (r *GovModelResolver) Resolve(ctx context.Context, workspaceID string) (llm.Provider, string) {
	if r == nil || r.db == nil || workspaceID == "" {
		return nil, ""
	}
	settings := governance.Resolve(ctx, r.db, r.logger, workspaceID)
	resolved, found := governance.ResolveGovModel(ctx, settings, workspaceID, r.lookup, r.dflt)
	if !found {
		return nil, "" // unconfigured — gatekeeper uses its default provider
	}
	if resolved.Degraded {
		r.emitDegrade(ctx, workspaceID, resolved.DegradeReason)
	}

	fp := govModelFingerprint(resolved)
	r.mu.Lock()
	// The API key is deliberately NOT part of the map key (see
	// govModelCacheEntry); a hit additionally requires the cached entry to
	// have been built from the same key, so a vault rotation rebuilds. Both
	// operands are vault-sourced server-side values, so a plain comparison
	// leaks nothing to a caller.
	if e, ok := r.cache[fp]; ok && e.apiKey == resolved.APIKey {
		r.mu.Unlock()
		return e.provider, resolved.Model
	}
	r.mu.Unlock()

	raw, err := r.buildProvider(resolved)
	if err != nil {
		// Building the configured provider failed unexpectedly (not a revoke —
		// that already degraded above). Fall back to the gatekeeper default so
		// the access path always has a working judge (fail-closed to a judge,
		// never fail-open to none).
		r.logger.Warn("keeper: gov-model provider build failed; using server default judge",
			"workspace_id", workspaceID, "provider", resolved.Provider, "error", err)
		return nil, ""
	}
	wrapped := llm.Middleware(raw, r.journal, r.db)

	r.mu.Lock()
	r.cache[fp] = govModelCacheEntry{apiKey: resolved.APIKey, provider: wrapped}
	r.mu.Unlock()
	return wrapped, resolved.Model
}

// GovModelStatus is the read-only governance-model state for the keeper status
// card. Configured=false means the workspace uses the server default judge.
type GovModelStatus struct {
	Configured bool
	Provider   string
	Model      string
	Degraded   bool
	Reason     string
}

// GovModelStatusProvider reports a workspace's resolved gov-model state without
// building a provider or emitting a WARN — the read-only seam the keeper status
// handler consumes.
type GovModelStatusProvider interface {
	Status(ctx context.Context, workspaceID string) GovModelStatus
}

// Status resolves the workspace's gov-model state for display. Unlike Resolve it
// builds no provider, mutates no cache, and emits no WARN (a read must be a
// read) — so the status card can report a degrade the request path surfaced.
func (r *GovModelResolver) Status(ctx context.Context, workspaceID string) GovModelStatus {
	if r == nil || r.db == nil || workspaceID == "" {
		return GovModelStatus{}
	}
	settings := governance.Resolve(ctx, r.db, r.logger, workspaceID)
	resolved, found := governance.ResolveGovModel(ctx, settings, workspaceID, r.lookup, r.dflt)
	if !found {
		return GovModelStatus{}
	}
	return GovModelStatus{
		Configured: true,
		Provider:   resolved.Provider,
		Model:      resolved.Model,
		Degraded:   resolved.Degraded,
		Reason:     resolved.DegradeReason,
	}
}

// buildProvider maps a resolved gov model to a concrete provider. Tenant
// endpoints (openai_compat, remote ollama sourced from a vault credential) dial
// through the SSRF fence; the degraded OLLAMA default (trusted server config,
// typically loopback) uses a plain client so it isn't blocked by the fence.
func (r *GovModelResolver) buildProvider(m governance.ResolvedGovModel) (llm.Provider, error) {
	switch m.Provider {
	case governance.ProviderOllama:
		// Degraded fallback or ollama-without-endpoint → trusted server default.
		if m.Degraded || m.EndpointURL == "" {
			url := m.EndpointURL
			if url == "" {
				url = r.dflt.URL
			}
			return llm.NewOllama(url, m.Model), nil
		}
		// Tenant-configured ollama endpoint → SSRF-fenced.
		return llm.NewOllamaWithClient(m.EndpointURL, m.Model, r.ssrf), nil
	case governance.ProviderAnthropic:
		key := m.APIKey
		if key == "" {
			key = os.Getenv("ANTHROPIC_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("anthropic gov model has no API key (vault credential or ANTHROPIC_API_KEY)")
		}
		return llm.NewAnthropic(key), nil
	case governance.ProviderOpenAICompat:
		if m.EndpointURL == "" {
			return nil, fmt.Errorf("openai_compat gov model has no endpoint URL (needs an ENDPOINT_URL credential)")
		}
		// SECURITY (key exfiltration): the endpoint here is tenant-admin
		// controlled (a workspace-configured URL), so we must NEVER attach the
		// server's OPENAI_API_KEY — that would leak the server key to an
		// arbitrary destination the admin chose. Use ONLY the vault-sourced key
		// (an API_KEY credential); send no key when none was configured (some
		// self-hosted endpoints need none) rather than falling back to env.
		return llm.NewOpenAIWithClient(m.APIKey, m.EndpointURL, r.ssrf), nil
	default:
		return nil, fmt.Errorf("unsupported gov model provider %q", m.Provider)
	}
}

// emitDegrade logs + journals a §4.4 degrade, de-duplicated per workspace so a
// stuck revoked credential doesn't flood the log/journal on every request.
func (r *GovModelResolver) emitDegrade(ctx context.Context, workspaceID, reason string) {
	r.mu.Lock()
	if r.warned[workspaceID] == reason {
		r.mu.Unlock()
		return
	}
	r.warned[workspaceID] = reason
	r.mu.Unlock()

	r.logger.Warn("keeper: governance model degraded to the default local judge",
		"workspace_id", workspaceID, "reason", reason)
	if r.journal != nil {
		_, _ = r.journal.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			Type:        journal.EntryKeeperDecision,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorKeeper,
			Summary:     "governance model degraded to the default local judge: " + reason,
			Payload:     map[string]any{"reason": reason, "rule": "gov_model_revoke_safety"},
		})
	}
}

// govModelFingerprint keys the build cache on every NON-secret field that
// changes the built provider. The API key is intentionally absent — it is
// matched against the cached govModelCacheEntry instead, so no secret (or
// secret-derived digest) ever appears in the map key.
func govModelFingerprint(m governance.ResolvedGovModel) string {
	return strings.Join([]string{
		m.Provider, m.Model, m.EndpointURL, strconv.FormatBool(m.Degraded),
	}, "|")
}

// govModelSSRFClient builds the #988-fenced http.Client used to dial a
// tenant-configured gov-model endpoint. Mirrors ollamaDiscoveryClient's dialer
// (respecting CREWSHIP_ALLOW_PRIVATE_ENDPOINTS) but with an LLM-appropriate
// completion timeout.
func govModelSSRFClient() *http.Client {
	allowPrivate := instanceAllowsPrivateEndpoints()
	return &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
				Control: func(_, address string, _ syscall.RawConn) error {
					host, _, err := net.SplitHostPort(address)
					if err != nil {
						return err
					}
					ip := httpsafe.ParseIPStripZone(host)
					if ip == nil {
						return nil
					}
					if httpsafe.IsBlockedIPForEndpoint(ip, allowPrivate) {
						return fmt.Errorf("governance model endpoint resolves to a blocked address (%s) — set CREWSHIP_ALLOW_PRIVATE_ENDPOINTS to reach a private endpoint", ip)
					}
					return nil
				},
			}).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Unwrapped as _ = gatekeeper.GovModelResolver(...) at wiring time; kept here as
// a compile-time assertion that Resolve matches the seam signature.
var _ gatekeeper.GovModelResolver = (*GovModelResolver)(nil).Resolve
