package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Resolve the request origin once: the network gate, the
		// master-token loopback pin (F-6), and the audit log all need it.
		host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
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
