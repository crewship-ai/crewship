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
	hub           *ws.Hub
	journal       journal.Emitter
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
		// Network gate first: refuse the request before constant-time
		// token compare so a public scanner can't even use this endpoint
		// to oracle the token's presence. Loopback always allowed (in-
		// process self-calls + operator SSH tunnel); private nets allowed
		// because Docker bridge IPs (172.x.x.x) and on-prem LANs land here.
		// CREWSHIP_INTERNAL_ALLOW_ANY=true bypasses entirely for setups
		// where a reverse proxy strips/spoofs RemoteAddr.
		if !allowAny {
			host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
			if splitErr != nil {
				host = r.RemoteAddr
			}
			ip := net.ParseIP(host)
			if ip == nil || (!ip.IsLoopback() && !ipInPrivateNet(ip)) {
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
			// Routes without a workspace_id query param pass through;
			// handlers that need the trusted scope read it via
			// InternalTokenWorkspaceFromContext.
			if q := r.URL.Query().Get("workspace_id"); q != "" && q != wsID {
				h.logger.Warn("internal API cross-workspace request refused",
					"path", r.URL.Path,
					"remote_addr", r.RemoteAddr,
					"token_workspace", wsID,
					"requested_workspace", q,
					"user_agent", r.Header.Get("User-Agent"))
				replyError(w, http.StatusForbidden, "Forbidden")
				return
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
		next.ServeHTTP(w, r)
	})
}
