package api

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// keeperWSBroadcaster adapts ws.Hub to the KeeperBroadcaster interface.
type keeperWSBroadcaster struct {
	hub *ws.Hub
}

func (b *keeperWSBroadcaster) BroadcastKeeperEvent(workspaceID string, event map[string]any) {
	channel := "keeper:" + workspaceID
	b.hub.Broadcast(channel, ws.ServerMessage{
		Type:    "keeper_event",
		Channel: channel,
		Payload: event,
	})
}

type Router struct {
	mux              *http.ServeMux
	db               *sql.DB
	logger           *slog.Logger
	authMw           *AuthMiddleware
	socketPath       string
	internalToken    string
	hub              *ws.Hub
	orch             *orchestrator.Orchestrator
	keeperGK         gatekeeper.Evaluator
	keeperSecrets    SecretGetter
	keeperContainer  provider.ContainerProvider
	keeperConfig     *config.KeeperConfig
	keeperConvReader ConversationReader
}

func NewRouter(db *sql.DB, jwtSecret string, logger *slog.Logger, opts ...RouterOption) (*Router, error) {
	validator, err := auth.NewJWTValidator(jwtSecret, "")
	if err != nil {
		return nil, err
	}

	authMw := NewAuthMiddleware(validator, db, logger)

	r := &Router{
		mux:    http.NewServeMux(),
		db:     db,
		logger: logger,
		authMw: authMw,
	}

	// Apply options before registering routes so that internalToken,
	// socketPath etc. are available during route setup.
	for _, opt := range opts {
		opt(r)
	}

	r.registerRoutes()

	return r, nil
}

type RouterOption func(*Router)

func WithStaticFS(webFS fs.FS) RouterOption {
	return func(r *Router) {
		r.mux.Handle("GET /", StaticFileHandler(webFS))
		r.logger.Info("serving embedded static UI")
	}
}

func WithSocketPath(path string) RouterOption {
	return func(r *Router) {
		r.socketPath = path
	}
}

func WithInternalToken(token string) RouterOption {
	return func(r *Router) {
		r.internalToken = token
	}
}

func WithHub(hub *ws.Hub) RouterOption {
	return func(r *Router) {
		r.hub = hub
	}
}

func WithOrchestrator(orch *orchestrator.Orchestrator) RouterOption {
	return func(r *Router) {
		r.orch = orch
	}
}

func WithKeeperGatekeeper(gk gatekeeper.Evaluator) RouterOption {
	return func(r *Router) {
		r.keeperGK = gk
	}
}

// WithKeeperSecrets attaches a SecretGetter to the router for the keeper execute handler.
// If not set, /keeper/execute will return 500 on ALLOW decisions (execute not configured).
func WithKeeperSecrets(sg SecretGetter) RouterOption {
	return func(r *Router) {
		r.keeperSecrets = sg
	}
}

// WithKeeperContainer attaches a ContainerProvider for the keeper execute handler.
// If not set, /keeper/execute will return 500 on ALLOW decisions (execute not configured).
func WithKeeperContainer(cp provider.ContainerProvider) RouterOption {
	return func(r *Router) {
		r.keeperContainer = cp
	}
}

// WithKeeperConfig passes Keeper configuration for the status endpoint.
func WithKeeperConfig(cfg *config.KeeperConfig) RouterOption {
	return func(r *Router) {
		r.keeperConfig = cfg
	}
}

// WithKeeperConversations attaches a conversation reader so Keeper can inspect
// the agent's actual chat history before making access decisions.
func WithKeeperConversations(cr ConversationReader) RouterOption {
	return func(r *Router) {
		r.keeperConvReader = cr
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) registerRoutes() {
	ws := NewWorkspaceHandler(r.db, r.logger)
	crews := NewCrewHandler(r.db, r.logger)
	agents := NewAgentHandler(r.db, r.logger)
	creds := NewCredentialHandler(r.db, r.logger)
	skills := NewSkillHandler(r.db, r.logger)
	runs := NewRunHandler(r.db, r.logger)
	audit := NewAuditHandler(r.db, r.logger)

	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	// Health (no auth)
	r.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// System info (auth required)
	system := NewSystemHandler(r.logger)
	r.mux.Handle("GET /api/v1/system/runtime", authed(http.HandlerFunc(system.Runtime)))

	// Keeper status (auth required)
	keeperStatus := NewKeeperStatusHandler(r.db, r.keeperConfig, r.keeperGK, r.logger)
	r.mux.Handle("GET /api/v1/system/keeper", authed(http.HandlerFunc(keeperStatus.Status)))

	// Workspaces (auth only, no workspace context needed)
	r.mux.Handle("GET /api/v1/workspaces", authed(http.HandlerFunc(ws.List)))
	r.mux.Handle("POST /api/v1/workspaces", authed(http.HandlerFunc(ws.Create)))

	// Workspace detail (require workspace context via path param)
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}", authed(wsCtx(http.HandlerFunc(ws.Get))))
	r.mux.Handle("PATCH /api/v1/workspaces/{workspaceId}", authed(wsCtx(http.HandlerFunc(ws.Update))))

	// Workspace members
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/members", authed(wsCtx(http.HandlerFunc(ws.ListMembers))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/members", authed(wsCtx(http.HandlerFunc(ws.AddMember))))
	r.mux.Handle("DELETE /api/v1/workspaces/{workspaceId}/members/{memberId}", authed(wsCtx(http.HandlerFunc(ws.RemoveMember))))

	// Workspace invitations
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/invitations", authed(wsCtx(http.HandlerFunc(ws.ListInvitations))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/invitations", authed(wsCtx(http.HandlerFunc(ws.CreateInvitation))))

	// Crews (require workspace context)
	r.mux.Handle("GET /api/v1/crews", authed(wsCtx(http.HandlerFunc(crews.List))))
	r.mux.Handle("POST /api/v1/crews", authed(wsCtx(http.HandlerFunc(crews.Create))))
	r.mux.Handle("GET /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Get))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Update))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Delete))))

	// Crew members
	r.mux.Handle("GET /api/v1/crews/{crewId}/members", authed(wsCtx(http.HandlerFunc(crews.ListMembers))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/members", authed(wsCtx(http.HandlerFunc(crews.AddMember))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/members/{memberId}", authed(wsCtx(http.HandlerFunc(crews.RemoveMember))))

	// Missions
	missions := NewMissionHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/missions", authed(wsCtx(http.HandlerFunc(missions.ListAll))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions", authed(wsCtx(http.HandlerFunc(missions.List))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions", authed(wsCtx(http.HandlerFunc(missions.Create))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Get))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Update))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Delete))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/tasks", authed(wsCtx(http.HandlerFunc(missions.CreateTask))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}", authed(wsCtx(http.HandlerFunc(missions.UpdateTask))))

	// Agents (require workspace context)
	r.mux.Handle("GET /api/v1/agents", authed(wsCtx(http.HandlerFunc(agents.List))))
	r.mux.Handle("POST /api/v1/agents", authed(wsCtx(http.HandlerFunc(agents.Create))))
	r.mux.Handle("GET /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Get))))
	r.mux.Handle("PATCH /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Update))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Delete))))

	// Agent skills
	r.mux.Handle("GET /api/v1/agents/{agentId}/skills", authed(wsCtx(http.HandlerFunc(agents.ListSkills))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/skills", authed(wsCtx(http.HandlerFunc(agents.AddSkill))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}/skills/{skillId}", authed(wsCtx(http.HandlerFunc(agents.RemoveSkill))))

	// Agent credentials
	r.mux.Handle("GET /api/v1/agents/{agentId}/credentials", authed(wsCtx(http.HandlerFunc(agents.ListCredentials))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/credentials", authed(wsCtx(http.HandlerFunc(agents.AddCredential))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}/credentials/{assignmentId}", authed(wsCtx(http.HandlerFunc(agents.RemoveCredential))))

	// Agent chats & runs
	r.mux.Handle("GET /api/v1/agents/{agentId}/chats", authed(wsCtx(http.HandlerFunc(agents.ListChats))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/chats", authed(wsCtx(http.HandlerFunc(agents.CreateChat))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/runs", authed(wsCtx(http.HandlerFunc(agents.ListRuns))))

	// Credentials (require workspace context + manage role for create)
	r.mux.Handle("GET /api/v1/credentials", authed(wsCtx(http.HandlerFunc(creds.List))))
	r.mux.Handle("POST /api/v1/credentials", authed(wsCtx(http.HandlerFunc(creds.Create))))
	r.mux.Handle("GET /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Get))))
	r.mux.Handle("PATCH /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Update))))
	r.mux.Handle("DELETE /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Delete))))

	// Skills (require auth)
	r.mux.Handle("GET /api/v1/skills", authed(wsCtx(http.HandlerFunc(skills.List))))
	r.mux.Handle("GET /api/v1/skills/{skillId}", authed(wsCtx(http.HandlerFunc(skills.Get))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/skills/import", authed(wsCtx(http.HandlerFunc(skills.Import))))

	// Runs (require workspace context)
	r.mux.Handle("GET /api/v1/runs", authed(wsCtx(http.HandlerFunc(runs.List))))

	// Audit logs (require workspace context + manage role)
	r.mux.Handle("GET /api/v1/audit", authed(wsCtx(http.HandlerFunc(audit.List))))

	// Onboarding (require auth, no workspace context needed)
	onboarding := NewOnboardingHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/onboarding/status", authed(http.HandlerFunc(onboarding.Status)))
	r.mux.Handle("POST /api/v1/onboarding/complete", authed(http.HandlerFunc(onboarding.Complete)))
	r.mux.Handle("POST /api/v1/onboarding/setup", authed(http.HandlerFunc(onboarding.Setup)))

	// Auth (no auth required)
	authH := NewAuthHandler(r.db, r.logger, r.authMw.validator)
	r.mux.HandleFunc("POST /api/v1/auth/signup", authH.Signup)
	r.mux.Handle("GET /api/v1/ws-token", authed(http.HandlerFunc(authH.WsToken)))

	// Auth endpoints (no RBAC -- public access required for login/signup flow).
	// These intentionally bypass RequireAuth as they are the authentication
	// bootstrap endpoints that establish the session cookie.
	nextAuth := NewNextAuthHandler(r.db, r.logger, r.authMw.validator)
	r.mux.HandleFunc("GET /api/auth/csrf", nextAuth.CSRF)
	r.mux.HandleFunc("GET /api/auth/providers", nextAuth.Providers)
	r.mux.HandleFunc("GET /api/auth/session", nextAuth.Session)
	r.mux.HandleFunc("POST /api/auth/callback/credentials", nextAuth.CallbackCredentials)
	r.mux.HandleFunc("GET /api/auth/signin", nextAuth.SignIn)
	r.mux.HandleFunc("POST /api/auth/signout", nextAuth.SignOut)
	r.mux.HandleFunc("GET /api/auth/error", nextAuth.Error)

	// Admin (require workspace context + OWNER)
	admin := NewAdminHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/admin/stats", authed(wsCtx(http.HandlerFunc(admin.Stats))))
	r.mux.Handle("GET /api/v1/admin/users", authed(wsCtx(http.HandlerFunc(admin.ListUsers))))
	r.mux.Handle("GET /api/v1/admin/workspaces", authed(wsCtx(http.HandlerFunc(admin.ListWorkspaces))))

	// Keeper admin log
	keeperLog := NewKeeperLogHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/admin/keeper/requests", authed(wsCtx(http.HandlerFunc(keeperLog.List))))

	// Crewshipd proxy + agent runtime routes (require IPC socket)
	socketPath := r.socketPath
	if socketPath == "" {
		socketPath = "/tmp/crewship.sock"
	}
	proxy := NewProxyHandler(r.db, r.logger, socketPath)
	r.mux.Handle("GET /api/v1/crewshipd", authed(wsCtx(http.HandlerFunc(proxy.CrewshipdHealth))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/debug", authed(wsCtx(http.HandlerFunc(proxy.AgentDebug))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/files", authed(wsCtx(http.HandlerFunc(proxy.AgentFiles))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/files/download", authed(wsCtx(http.HandlerFunc(proxy.AgentFileDownload))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/logs", authed(wsCtx(http.HandlerFunc(proxy.AgentLogs))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/stop", authed(wsCtx(http.HandlerFunc(proxy.AgentStop))))
	r.mux.Handle("GET /api/v1/chats/{chatId}/messages", authed(http.HandlerFunc(proxy.ChatMessages)))

	// Internal routes (for crewshipd IPC, X-Internal-Token auth)
	internal := NewInternalHandler(r.db, r.internalToken, r.logger)
	if r.keeperConfig != nil && r.keeperConfig.Enabled {
		internal.SetKeeperEnabled(true)
	}
	internalAuth := internal.requireInternal
	r.mux.Handle("GET /api/v1/internal/credentials", internalAuth(http.HandlerFunc(internal.ListCredentials)))
	r.mux.Handle("PATCH /api/v1/internal/credentials/{credentialId}", internalAuth(http.HandlerFunc(internal.UpdateCredentialStatus)))
	r.mux.Handle("POST /api/v1/internal/chats", internalAuth(http.HandlerFunc(internal.CreateChat)))
	r.mux.Handle("GET /api/v1/internal/chats/{chatId}/resolve", internalAuth(http.HandlerFunc(internal.ResolveChat)))
	r.mux.Handle("POST /api/v1/internal/runs", internalAuth(http.HandlerFunc(internal.CreateRun)))
	r.mux.Handle("PATCH /api/v1/internal/runs/{runId}", internalAuth(http.HandlerFunc(internal.UpdateRun)))
	r.mux.Handle("PATCH /api/v1/internal/chats/{chatId}/message-count", internalAuth(http.HandlerFunc(internal.IncrementMessageCount)))

	// Assignment routes (internal auth, called by sidecar on behalf of lead agents)
	assign := NewAssignmentHandler(r.db, r.orch, r.hub, r.internalToken, r.logger)
	r.mux.Handle("POST /api/v1/internal/assignments", internalAuth(http.HandlerFunc(assign.Create)))
	r.mux.Handle("GET /api/v1/internal/assignments/{assignmentId}", internalAuth(http.HandlerFunc(assign.Get)))

	// Crew assignments (public, authenticated)
	r.mux.Handle("GET /api/v1/crews/{crewId}/assignments", authed(wsCtx(http.HandlerFunc(assign.List))))

	// Query routes (peer-to-peer communication, standup summaries, escalations)
	queries := NewQueryHandler(r.db, r.orch, r.hub, r.internalToken, r.logger)
	r.mux.Handle("POST /api/v1/internal/queries", internalAuth(http.HandlerFunc(queries.Create)))
	r.mux.Handle("GET /api/v1/internal/standup", internalAuth(http.HandlerFunc(queries.Standup)))
	r.mux.Handle("POST /api/v1/internal/escalations", internalAuth(http.HandlerFunc(queries.CreateEscalation)))

	// Crew peer conversations, standup, and escalations (public, authenticated)
	r.mux.Handle("GET /api/v1/crews/{crewId}/peer-conversations", authed(wsCtx(http.HandlerFunc(queries.ListPeerConversations))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/standup", authed(wsCtx(http.HandlerFunc(queries.Standup))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/escalations", authed(wsCtx(http.HandlerFunc(queries.ListEscalations))))

	// Cross-crew activity feed (public, authenticated)
	r.mux.Handle("GET /api/v1/activity", authed(wsCtx(http.HandlerFunc(queries.ListAllActivity))))

	// Keeper — credential access control (internal auth)
	keeperH := NewKeeperHandler(r.db, r.internalToken, r.keeperGK, r.logger).
		WithSecrets(r.keeperSecrets).
		WithContainer(r.keeperContainer).
		WithConversations(r.keeperConvReader)
	if r.hub != nil {
		keeperH.WithBroadcaster(&keeperWSBroadcaster{hub: r.hub})
	}
	r.mux.Handle("POST /api/v1/internal/keeper/request", internalAuth(http.HandlerFunc(keeperH.HandleRequest)))
	r.mux.Handle("GET /api/v1/internal/keeper/request/{requestId}", internalAuth(http.HandlerFunc(keeperH.GetRequest)))
	r.mux.Handle("POST /api/v1/internal/keeper/execute", internalAuth(http.HandlerFunc(keeperH.HandleExecute)))
}
