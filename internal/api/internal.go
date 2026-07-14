package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// privateNetCIDRs lists the address ranges the internal API will accept
// connections from when CREWSHIP_INTERNAL_ALLOW_ANY is unset. The list
// covers IPv4 RFC1918 (where Docker bridge subnets and on-prem LANs live)
// plus IPv6 ULA and link-local. Loopback is handled separately via
// IP.IsLoopback so we don't have to list 127.0.0.0/8 and ::1 twice.
//
// Parsing happens once at init — these literals are baked in, so a
// startup-time MustParseCIDR would only panic on developer typos, which
// is exactly the behaviour we want. No runtime config knob lives in
// here; operators flip CREWSHIP_INTERNAL_ALLOW_ANY=true to bypass.
var privateNetCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16", // link-local (Docker on macOS sometimes routes via)
		"fc00::/7",       // IPv6 ULA
		"fe80::/10",      // IPv6 link-local
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("internal API: invalid private CIDR " + c + ": " + err.Error())
		}
		nets = append(nets, n)
	}
	return nets
}()

// ipInPrivateNet reports whether ip falls in any of the RFC1918-ish
// ranges we treat as "may legitimately reach the internal API".
// Loopback is not checked here — the caller handles that bucket
// separately so the audit-log message can distinguish them.
func ipInPrivateNet(ip net.IP) bool {
	for _, n := range privateNetCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseInternalTrustedProxies parses CREWSHIP_INTERNAL_TRUSTED_PROXIES — a
// comma-separated list of CIDRs or bare IPs — into networks. Empty/unset
// returns nil (#1020): the trusted set is EXPLICIT and never auto-populated
// with private ranges, because auto-trusting 10.0.0.0/8 on a Proxmox/on-prem
// LAN would let any host on that LAN spoof X-Forwarded-For and erase the origin
// gate. (This is deliberately distinct from the rate limiter's
// parseTrustedProxies, which defaults to trusting loopback — a lower-stakes
// client-attribution concern.) A malformed entry is logged and skipped, never
// silently widened.
func parseInternalTrustedProxies(csv string, logger *slog.Logger) []*net.IPNet {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	var nets []*net.IPNet
	for _, raw := range strings.Split(csv, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			// Bare IP → host route (/32 or /128).
			if ip := net.ParseIP(entry); ip != nil {
				if ip4 := ip.To4(); ip4 != nil {
					entry += "/32"
				} else {
					entry += "/128"
				}
			}
		}
		_, n, err := net.ParseCIDR(entry)
		if err != nil {
			if logger != nil {
				logger.Error("CREWSHIP_INTERNAL_TRUSTED_PROXIES: ignoring invalid entry",
					"entry", raw, "error", err)
			}
			continue
		}
		// Reject an all-zeros CIDR (0.0.0.0/0, ::/0). "Trust every proxy" is
		// almost always a misconfig, and it specifically enables a cross-family
		// bypass: net.IPNet.Contains is family-blind, so a trusted 0.0.0.0/0
		// would treat a spoofed X-Forwarded-For: ::1 from any IPv4 peer as a
		// loopback client and pass the gate. Drop it loudly.
		if ones, _ := n.Mask.Size(); ones == 0 {
			if logger != nil {
				logger.Error("CREWSHIP_INTERNAL_TRUSTED_PROXIES: refusing all-zeros CIDR (would trust every peer as a proxy)",
					"entry", raw)
			}
			continue
		}
		nets = append(nets, n)
	}
	if logger != nil && len(nets) > 0 {
		strs := make([]string, len(nets))
		for i, n := range nets {
			strs[i] = n.String()
		}
		// Log the actual ranges (not just a count) so a /8-vs-/32 typo is
		// visible to operators auditing the trust set.
		logger.Info("internal API: trusted reverse-proxy ranges configured", "cidrs", strings.Join(strs, ","))
	}
	return nets
}

// ipInNets reports whether ip falls in any of nets.
func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// rightmostUntrustedXFF resolves the real client IP from X-Forwarded-For when
// the direct peer is a trusted proxy (#1020). It flattens all XFF header values
// (comma-split, in order) and walks RIGHT→LEFT — the rightmost hops are the
// ones our own trusted proxies appended, so the first hop NOT in the trusted
// set is the genuine client; anything left of it is attacker-controlled and
// ignored. Fails CLOSED (ok=false) on an empty header, an all-trusted chain, or
// an unparseable hop — the caller then denies rather than falling back to the
// proxy's own loopback/private RemoteAddr (which would pass the gate).
func rightmostUntrustedXFF(xffHeaders []string, trusted []*net.IPNet) (net.IP, bool) {
	var hops []string
	for _, h := range xffHeaders {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				hops = append(hops, p)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(hops[i])
		if ip == nil {
			// A trusted proxy wrote a non-IP token — treat the whole chain as
			// untrustworthy and fail closed.
			return nil, false
		}
		if ipInNets(ip, trusted) {
			continue // another trusted proxy hop; keep walking left
		}
		return ip, true // rightmost untrusted hop = the real client
	}
	return nil, false
}

type mcpCredEntry struct {
	ID       string `json:"id"`
	EnvVar   string `json:"env_var"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
	// Username carries the cleartext identifier half of a USERPASS
	// credential. Always empty for other types — the sidecar mount
	// path branches on Type and only reads Username for USERPASS.
	Username string `json:"username,omitempty"`
}

// InternalHandler provides endpoints called by the sidecar over the Unix socket using X-Internal-Token auth.
type InternalHandler struct {
	db            *sql.DB
	logger        *slog.Logger
	internalToken string
	keeperEnabled atomic.Bool
	// composioDefaultConnector mirrors config.ComposioConfig.DefaultConnector
	// (COMPOSIO_DEFAULT_CONNECTOR). When true, resolveAgentMCPServers turns
	// legacy (non-Composio) MCP servers off and injects a workspace-wide
	// default Composio connector for agents without a per-agent binding.
	// Off = today's resolve behaviour, byte-for-byte. composioBaseURL is the
	// server-env Composio base URL used only to build the default MCP
	// transport URL (no API call) in the hot path.
	composioDefaultConnector atomic.Bool
	composioBaseURL          atomic.Value // string
	hub                      *ws.Hub
	journal                  journal.Emitter
	// postRunTrigger fires the memory consolidator opportunistically
	// after each successful run.completed emit. nil → no trigger (the
	// 6h cron stays as the safety net). Wired via SetPostRunTrigger
	// from router_orchestration.go once the consolidator is ready.
	postRunTrigger postRunTriggerHook
	// policyResolver maps the resolved agent's crew autonomy_level to the
	// harbormaster HITL gate mode surfaced as approval_mode in the resolve
	// response (#810). nil → approval_mode is "" (ModeNone), i.e. today's
	// behaviour — so tests that construct the handler without a resolver are
	// unaffected. Wired at router build time.
	policyResolver *policy.Resolver
	// container reaches running crew containers so a status transition to
	// REVOKED removes materialized /secrets files, same as the public DELETE
	// handler (#814). nil (tests, --no-docker) → reconciliation no-ops.
	container provider.ContainerProvider
	// reconcileWG tracks the async revoke-reconcile goroutines spawned by
	// UpdateCredentialStatus so tests (and a graceful shutdown, if it ever
	// wants to) can wait for them instead of sleeping.
	reconcileWG sync.WaitGroup
	// allowAnyWarnOnce gates the CREWSHIP_INTERNAL_ALLOW_ANY startup warning
	// in requireInternal so it logs once per handler, not once per wrapped
	// route (~52 routes share this one *InternalHandler instance, see
	// registerInternalRoutes's single `internalAuth := internal.requireInternal`).
	allowAnyWarnOnce sync.Once
}

// postRunTriggerHook is the narrow interface UpdateRun calls. The
// concrete impl lives in internal/consolidate.PostRunTrigger; this
// interface keeps the api package from importing the consolidate
// package's heavyweight transitive dependencies (LLM, episodic, ...).
type postRunTriggerHook interface {
	OnRunCompleted(ctx context.Context, workspaceID, crewID, crewSlug string) bool
}

// NewInternalHandler creates an InternalHandler with the given database, internal token, and logger.
// Callers that want journal emits wire them after construction with SetJournal;
// the default is a no-op so tests stay simple.
func NewInternalHandler(db *sql.DB, internalToken string, logger *slog.Logger) *InternalHandler {
	return &InternalHandler{db: db, internalToken: internalToken, logger: logger, journal: noopEmitter{}}
}

// SetHub attaches a WebSocket hub for broadcasting events from internal endpoints.
func (h *InternalHandler) SetHub(hub *ws.Hub) {
	h.hub = hub
}

// SetJournal wires a journal emitter. nil maps to the no-op so callers
// don't have to branch on whether the server wired one.
func (h *InternalHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// SetKeeperEnabled toggles whether Keeper is advertised as enabled in the agent config.
func (h *InternalHandler) SetKeeperEnabled(enabled bool) {
	h.keeperEnabled.Store(enabled)
}

// SetContainer wires the container provider used to remove a revoked
// credential's file-based /secrets entries from running containers when the
// sidecar reports status REVOKED (#814 parity with the DELETE handler).
func (h *InternalHandler) SetContainer(cp provider.ContainerProvider) { h.container = cp }

// SetPolicyResolver wires the per-crew autonomy policy resolver used to
// derive approval_mode in the agent-config resolve response (#810). nil is
// safe — approval_mode falls back to "" (harbormaster ModeNone).
func (h *InternalHandler) SetPolicyResolver(r *policy.Resolver) {
	h.policyResolver = r
}

// SetComposioDefaultConnector arms (or disarms) the default-connector
// behaviour in resolveAgentMCPServers and pins the Composio base URL used to
// build the default MCP transport URL. baseURL may be empty (the resolver
// falls back to the Composio production host via the client). Wired at router
// build time from config.ComposioConfig.
func (h *InternalHandler) SetComposioDefaultConnector(enabled bool, baseURL string) {
	h.composioDefaultConnector.Store(enabled)
	h.composioBaseURL.Store(baseURL)
}

// composioBase returns the configured Composio base URL (empty when unset).
func (h *InternalHandler) composioBase() string {
	if v, ok := h.composioBaseURL.Load().(string); ok {
		return v
	}
	return ""
}

// SetPostRunTrigger wires the memory→consolidator post-run hook. Pass
// nil (or skip the call) to disable the sleep-time trigger entirely;
// the 6h cron remains as the safety net. The hook is consulted in
// UpdateRun after a successful run.completed emit.
func (h *InternalHandler) SetPostRunTrigger(t postRunTriggerHook) {
	h.postRunTrigger = t
}

func (h *InternalHandler) requireInternal(next http.Handler) http.Handler {
	if h.internalToken == "" {
		h.logger.Error("internal token is empty -- all internal API calls will be rejected")
	}
	// Resolve the allow-any kill-switch once per handler build so we don't
	// touch os.Getenv on every request. Set to true ONLY when crewshipd is
	// deployed behind a trusted reverse proxy on a public interface and
	// the operator has accepted that X-Internal-Token is the sole guard.
	allowAny := os.Getenv("CREWSHIP_INTERNAL_ALLOW_ANY") == "true"
	if allowAny {
		// #1083: surface the kill-switch at startup. With the origin gate off,
		// X-Internal-Token is the sole guard — and the include_values=true
		// plaintext credential readback still trusts requestIsLoopback(
		// r.RemoteAddr). Behind a reverse proxy that rewrites RemoteAddr to a
		// loopback address, that assumption would no longer hold. Only enable
		// ALLOW_ANY behind a trusted proxy that preserves the real client addr.
		//
		// requireInternal wraps ~52 routes on this one *InternalHandler
		// instance (registerInternalRoutes builds a single `internalAuth`
		// and reuses it), so without allowAnyWarnOnce this logs 52 identical
		// lines at startup.
		h.allowAnyWarnOnce.Do(func() {
			h.logger.Warn("CREWSHIP_INTERNAL_ALLOW_ANY=true: internal-API origin gate disabled; X-Internal-Token is the sole guard. " +
				"Ensure the reverse proxy does not rewrite RemoteAddr to loopback — the include_values plaintext readback gates on it.")
		})
	}
	// #1020: trusted-proxy X-Forwarded-For resolution. Empty/unset = today's
	// behaviour (gate on the direct RemoteAddr only). The set is EXPLICIT and
	// never auto-populated with private ranges — see parseTrustedProxies.
	trustedProxies := parseInternalTrustedProxies(os.Getenv("CREWSHIP_INTERNAL_TRUSTED_PROXIES"), h.logger)
	if len(trustedProxies) > 0 {
		h.logger.Info("internal API: X-Forwarded-For origin resolution enabled for trusted reverse proxies",
			"trusted_proxy_ranges", len(trustedProxies))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Resolve the request origin once: the network gate, the
		// master-token loopback pin (F-6), and the audit log all need it.
		host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			host = r.RemoteAddr
		}
		directIP := net.ParseIP(host)

		// #1020: behind a reverse proxy the direct peer is the proxy itself
		// (Caddy/nginx, often loopback), so gating on RemoteAddr alone lets
		// any public client through the network gate — the only guard left is
		// then the shared X-Internal-Token. When the DIRECT peer is a
		// configured trusted proxy, resolve the real client from
		// X-Forwarded-For (rightmost UNTRUSTED hop) and gate on that instead.
		// XFF is consulted ONLY for a trusted-proxy peer — a client connecting
		// directly (untrusted RemoteAddr) can't spoof it. If a trusted proxy
		// forwards no usable client hop (missing/empty/garbage XFF) we fail
		// CLOSED rather than fall back to the proxy's own loopback/private
		// address, which would sail through the gate.
		//
		// A forwarding header is resolved only when actually PRESENT: a trusted
		// proxy forwarding real traffic always sets X-Forwarded-For (chain,
		// preferred) or at least X-Real-IP (nginx's single-IP default). Prefer
		// XFF (chain-aware, rightmost-untrusted); fall back to X-Real-IP. If a
		// header is present but carries no usable client (empty / all-trusted /
		// garbage) we fail CLOSED. If NEITHER header is present, the request is
		// a same-host self-call (crewshipd's WithInternalLoopbackURL sends
		// neither) → gate on the direct IP (loopback → allowed). Only honored
		// for a trusted-proxy peer — a direct client can't spoof either header.
		ip := directIP
		if len(trustedProxies) > 0 && directIP != nil && ipInNets(directIP, trustedProxies) {
			if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
				realIP, ok := rightmostUntrustedXFF(xff, trustedProxies)
				if !ok {
					h.logger.Warn("internal API: trusted proxy forwarded no usable client IP in X-Forwarded-For — refused (fail-closed)",
						"path", r.URL.Path, "remote_addr", r.RemoteAddr,
						"user_agent", r.Header.Get("User-Agent"))
					replyError(w, http.StatusNotFound, "Not Found")
					return
				}
				ip = realIP
			} else if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
				// A trusted proxy set X-Real-IP to the real client (nginx
				// default). Single value from a trusted peer; fail closed on a
				// non-IP so a garbage header can't fall through to the proxy IP.
				realIP := net.ParseIP(xrip)
				if realIP == nil {
					h.logger.Warn("internal API: trusted proxy sent an unparseable X-Real-IP — refused (fail-closed)",
						"path", r.URL.Path, "remote_addr", r.RemoteAddr)
					replyError(w, http.StatusNotFound, "Not Found")
					return
				}
				ip = realIP
			}
			// else: neither header → same-host self-call → gate on directIP.
		}
		isLoopback := ip != nil && ip.IsLoopback()

		// Network gate first: refuse the request before constant-time
		// token compare so a public scanner can't even use this endpoint
		// to oracle the token's presence. Loopback always allowed (in-
		// process self-calls + operator SSH tunnel); private nets allowed
		// because Docker bridge IPs (172.x.x.x) and on-prem LANs land here.
		// CREWSHIP_INTERNAL_ALLOW_ANY=true bypasses entirely for setups
		// where a reverse proxy strips/spoofs RemoteAddr.
		if !allowAny {
			if ip == nil || (!isLoopback && !ipInPrivateNet(ip)) {
				h.logger.Warn("internal API access from non-internal IP — refused",
					"path", r.URL.Path,
					"remote_addr", r.RemoteAddr,
					"user_agent", r.Header.Get("User-Agent"))
				// 404 instead of 403 to avoid confirming the endpoint
				// exists to a public scanner. Legitimate callers see
				// this only when misconfigured.
				replyError(w, http.StatusNotFound, "Not Found")
				return
			}
		}

		token := r.Header.Get("X-Internal-Token")

		// Crew-bound token path (#1159). A per-crew sidecar's token binds
		// BOTH the workspace and the crew — crwv1.<ws>.<crew>.<mac> — so
		// the middleware can pin the crew scope server-side instead of
		// trusting a caller-supplied ?crew_id (which any workspace-bound
		// holder could forge to enumerate every crew's credential
		// metadata). Checked before the workspace branch; the "crwv1."
		// prefix never collides with "wsv1.".
		if internaltoken.IsCrewToken(token) {
			wsID, crewID, ok := internaltoken.ValidateCrewToken(h.internalToken, token)
			if !ok {
				h.logger.Warn("internal API auth failed: invalid crew-bound token",
					"path", r.URL.Path,
					"remote_addr", r.RemoteAddr,
					"user_agent", r.Header.Get("User-Agent"))
				replyError(w, http.StatusForbidden, "Forbidden")
				return
			}
			q := r.URL.Query()
			// Workspace scope — identical mandatory-scope injection to the
			// workspace-bound path below: a disagreeing ?workspace_id is a
			// cross-tenant forgery (403); an omitted one is injected so every
			// ?workspace_id-filtered handler runs tenant-scoped.
			if reqWS := q.Get("workspace_id"); reqWS != "" {
				if reqWS != wsID {
					h.logger.Warn("internal API cross-workspace request refused (crew token)",
						"path", r.URL.Path, "remote_addr", r.RemoteAddr,
						"token_workspace", wsID, "requested_workspace", reqWS)
					replyError(w, http.StatusForbidden, "Forbidden")
					return
				}
			} else {
				q.Set("workspace_id", wsID)
			}
			// Crew scope — a crew-bound token pins the crew. A caller-supplied
			// ?crew_id that disagrees is the enumerate-a-sibling-crew forgery
			// #1159 closes (403). We do NOT inject a crew_id when omitted:
			// injecting would silently narrow the many OTHER internal endpoints
			// that scope OPTIONALLY by crew_id (status, issues, missions), a
			// behaviour change beyond this issue's scope. The credential
			// listing — the endpoint that mattered — instead reads the
			// crew from context (InternalTokenCrewFromContext), which is
			// authoritative and unforgeable regardless of the query.
			if reqCrew := q.Get("crew_id"); reqCrew != "" && reqCrew != crewID {
				h.logger.Warn("internal API cross-crew request refused (crew token)",
					"path", r.URL.Path, "remote_addr", r.RemoteAddr,
					"token_crew", crewID, "requested_crew", reqCrew)
				replyError(w, http.StatusForbidden, "Forbidden")
				return
			}
			r.URL.RawQuery = q.Encode()
			ctx := context.WithValue(r.Context(), ctxInternalTokenWS, wsID)
			ctx = context.WithValue(ctx, ctxInternalTokenCrew, crewID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Workspace-bound token path (PR-F24). Sidecars no longer hold
		// the master token — at sidecar start the orchestrator hands
		// each one HMAC(master, workspace_id) instead, so a token
		// captured inside a container only authorizes its own
		// workspace. The prefix check is on a public format marker,
		// not a secret; the MAC verification inside
		// ValidateWorkspaceToken is constant-time.
		if internaltoken.IsWorkspaceToken(token) {
			wsID, ok := internaltoken.ValidateWorkspaceToken(h.internalToken, token)
			if !ok {
				h.logger.Warn("internal API auth failed: invalid workspace-bound token",
					"path", r.URL.Path,
					"remote_addr", r.RemoteAddr,
					"user_agent", r.Header.Get("User-Agent"))
				replyError(w, http.StatusForbidden, "Forbidden")
				return
			}
			// The binding enforcement: a caller-supplied workspace_id
			// that disagrees with the token's workspace is the
			// cross-tenant forgery this token format exists to stop.
			//
			// Mandatory-scope injection (PR-F24 hardening): a bound
			// token is the workspace scope. When the caller supplies a
			// query workspace_id it must agree (403 on mismatch); when
			// it omits one, we INJECT the bound workspace into the query
			// so every handler that filters by ?workspace_id (webhook
			// secret, list credentials, agent resolve, …) becomes
			// tenant-scoped automatically instead of silently running
			// unscoped. There is no "legacy unscoped" fall-through for
			// bound tokens any more. Path-param handlers that don't read
			// the query consult InternalTokenWorkspaceFromContext.
			q := r.URL.Query()
			if reqWS := q.Get("workspace_id"); reqWS != "" {
				if reqWS != wsID {
					h.logger.Warn("internal API cross-workspace request refused",
						"path", r.URL.Path,
						"remote_addr", r.RemoteAddr,
						"token_workspace", wsID,
						"requested_workspace", reqWS,
						"user_agent", r.Header.Get("User-Agent"))
					replyError(w, http.StatusForbidden, "Forbidden")
					return
				}
			} else {
				q.Set("workspace_id", wsID)
				r.URL.RawQuery = q.Encode()
			}
			ctx := context.WithValue(r.Context(), ctxInternalTokenWS, wsID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Master-token path — host-side trusted callers (chatbridge
		// resolver, llmproxy monitor) that never enter a container.
		// Always run constant-time comparison to avoid timing sidechannels.
		// Pad empty strings to a fixed sentinel so the comparison still runs
		// in constant time even when token or internalToken is empty.
		expected := h.internalToken
		if expected == "" {
			expected = "\x00empty-sentinel\x00"
		}
		actual := token
		if actual == "" {
			actual = "\x00different-sentinel\x00"
		}
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			// Audit trail for failed internal-API access — H2 mitigation.
			// An agent inside a crew container that learns the token
			// (UID escalation / memory dump / shared file) can call
			// /api/v1/internal/*; this WARN entry is the first place an
			// operator sees that activity. logger goes through slog so
			// the sink (file / journald / shipped log) catches it even
			// when the journal emitter is the no-op.
			h.logger.Warn("internal API auth failed",
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
				"token_present", token != "",
				"user_agent", r.Header.Get("User-Agent"))
			replyError(w, http.StatusForbidden, "Forbidden")
			return
		}

		// Master-token origin pin (PR-F24 / F-6). The unbound master
		// authorizes EVERY workspace (its bound scope is ""), so a copy
		// leaked into a crew container would otherwise retain full
		// cross-tenant power — the exact blast radius the per-workspace
		// derived tokens exist to cap. The only legitimate master-token
		// callers are host-side trusted services (chat-bridge resolver,
		// LLM-proxy cost monitor, webhook secret resolver) that dial the
		// internal API in-process over loopback (127.0.0.1 / ::1; see
		// WithInternalLoopbackURL and the default NextjsURL). Sidecars,
		// which reach us from a Docker-bridge / LAN IP, always carry a
		// workspace-bound token and took the branch above. So a master
		// token arriving from a non-loopback origin is, by construction,
		// either a leak being replayed from inside a container or a
		// misconfiguration — refuse it. Operators who front crewshipd
		// with a reverse proxy that rewrites RemoteAddr opt back in with
		// CREWSHIP_INTERNAL_ALLOW_ANY=true (the same kill-switch that
		// relaxes the network gate), accepting that the token is then
		// the sole guard.
		if !allowAny && !isLoopback {
			h.logger.Warn("internal API master token from non-loopback origin refused",
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
				"user_agent", r.Header.Get("User-Agent"))
			replyError(w, http.StatusForbidden, "Forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}
