package api

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/logcollector"
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
	internalBaseURL  string
	hub              *ws.Hub
	orch             *orchestrator.Orchestrator
	keeperGK         gatekeeper.Evaluator
	keeperSecrets    SecretGetter
	keeperContainer  provider.ContainerProvider
	keeperConfig     *config.KeeperConfig
	keeperConvReader ConversationReader
	missionCallback  MissionCallback
	scheduleUpdater  ScheduleUpdater
	logWriter        *logcollector.Writer
	captainLLM           llm.Provider
	captainMissionEngine MissionStarter
	allowSignup          bool
	license          *license.License
	agentHandler     *AgentHandler
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

// SetScheduler attaches a ScheduleUpdater after construction (used by cmd_start).
func (r *Router) SetScheduler(su ScheduleUpdater) {
	r.scheduleUpdater = su
	if r.agentHandler != nil {
		r.agentHandler.SetScheduler(su)
	}
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

func WithInternalBaseURL(url string) RouterOption {
	return func(r *Router) {
		r.internalBaseURL = url
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

func WithScheduler(su ScheduleUpdater) RouterOption {
	return func(r *Router) {
		r.scheduleUpdater = su
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
func WithAllowSignup(allow bool) RouterOption {
	return func(r *Router) {
		r.allowSignup = allow
	}
}

func WithKeeperConversations(cr ConversationReader) RouterOption {
	return func(r *Router) {
		r.keeperConvReader = cr
	}
}

func WithMissionCallback(cb MissionCallback) RouterOption {
	return func(r *Router) {
		r.missionCallback = cb
	}
}

func WithLogWriter(lw *logcollector.Writer) RouterOption {
	return func(r *Router) {
		r.logWriter = lw
	}
}

func WithCaptainLLM(p llm.Provider) RouterOption {
	return func(r *Router) {
		r.captainLLM = p
	}
}

func WithCaptainMissionCallback(ms MissionStarter) RouterOption {
	return func(r *Router) {
		r.captainMissionEngine = ms
	}
}

func WithLicense(lic *license.License) RouterOption {
	return func(r *Router) {
		r.license = lic
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) registerRoutes() {
	ws := NewWorkspaceHandler(r.db, r.logger)
	crews := NewCrewHandler(r.db, r.logger)
	crewSocket := r.socketPath
	if crewSocket == "" {
		crewSocket = "/tmp/crewship.sock"
	}
	crews.SetSocketPath(crewSocket)
	if r.hub != nil {
		crews.SetHub(r.hub)
	}
	agents := NewAgentHandler(r.db, r.logger)
	r.agentHandler = agents
	if r.hub != nil {
		agents.SetHub(r.hub)
	}
	if r.scheduleUpdater != nil {
		agents.SetScheduler(r.scheduleUpdater)
	}

	if r.license != nil {
		ws.SetLicense(r.license)
		crews.SetLicense(r.license)
		agents.SetLicense(r.license)
	}
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

	// License info (auth required)
	licenseH := NewLicenseHandler(r.license)
	r.mux.Handle("GET /api/v1/system/license", authed(http.HandlerFunc(licenseH.Status)))

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
	r.mux.Handle("PUT /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Update))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Delete))))

	// Crew members
	r.mux.Handle("GET /api/v1/crews/{crewId}/members", authed(wsCtx(http.HandlerFunc(crews.ListMembers))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/members", authed(wsCtx(http.HandlerFunc(crews.AddMember))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/members/{memberId}", authed(wsCtx(http.HandlerFunc(crews.RemoveMember))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/apply-avatar-style", authed(wsCtx(http.HandlerFunc(crews.ApplyAvatarStyle))))

	// Crew Connections
	conns := NewCrewConnectionHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/crew-connections", authed(wsCtx(http.HandlerFunc(conns.List))))
	r.mux.Handle("POST /api/v1/crew-connections", authed(wsCtx(http.HandlerFunc(conns.Create))))
	r.mux.Handle("DELETE /api/v1/crew-connections/{connectionId}", authed(wsCtx(http.HandlerFunc(conns.Delete))))

	// Integrations (MCP Gateway)
	integrations := NewIntegrationHandler(r.db, r.logger)
	if r.hub != nil {
		integrations.SetHub(r.hub)
	}
	// Workspace-level integrations
	r.mux.Handle("GET /api/v1/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListWorkspaceIntegrations))))
	r.mux.Handle("POST /api/v1/integrations", authed(wsCtx(http.HandlerFunc(integrations.CreateWorkspaceIntegration))))
	r.mux.Handle("GET /api/v1/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.GetWorkspaceIntegration))))
	r.mux.Handle("PATCH /api/v1/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.UpdateWorkspaceIntegration))))
	r.mux.Handle("DELETE /api/v1/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.DeleteWorkspaceIntegration))))
	// All crew integrations (cross-crew overview for Integrations page)
	r.mux.Handle("GET /api/v1/integrations/crews", authed(wsCtx(http.HandlerFunc(integrations.ListAllCrewIntegrations))))
	// Crew-level integrations
	r.mux.Handle("GET /api/v1/crews/{crewId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListCrewIntegrations))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.CreateCrewIntegration))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.UpdateCrewIntegration))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.DeleteCrewIntegration))))
	// Agent MCP bindings
	r.mux.Handle("GET /api/v1/agents/{agentId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListAgentBindings))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.CreateAgentBinding))))
	r.mux.Handle("PATCH /api/v1/agents/{agentId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.UpdateAgentBinding))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.DeleteAgentBinding))))
	// Resolve effective integrations for an agent (cascade: workspace → crew → agent bindings)
	r.mux.Handle("GET /api/v1/agents/{agentId}/integrations/resolved", authed(wsCtx(http.HandlerFunc(integrations.ResolveAgentIntegrations))))

	// Workflow Templates
	templates := NewTemplateHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/templates", authed(wsCtx(http.HandlerFunc(templates.List))))
	r.mux.Handle("POST /api/v1/templates", authed(wsCtx(http.HandlerFunc(templates.Create))))
	r.mux.Handle("GET /api/v1/templates/{templateId}", authed(wsCtx(http.HandlerFunc(templates.Get))))
	r.mux.Handle("PATCH /api/v1/templates/{templateId}", authed(wsCtx(http.HandlerFunc(templates.Update))))
	r.mux.Handle("DELETE /api/v1/templates/{templateId}", authed(wsCtx(http.HandlerFunc(templates.Delete))))

	// Crew Templates (blueprints)
	crewTmpl := NewCrewTemplateHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/crew-templates", authed(wsCtx(http.HandlerFunc(crewTmpl.List))))
	r.mux.Handle("GET /api/v1/crew-templates/{slug}", authed(wsCtx(http.HandlerFunc(crewTmpl.Get))))
	r.mux.Handle("POST /api/v1/crew-templates/{slug}/deploy", authed(wsCtx(http.HandlerFunc(crewTmpl.Deploy))))

	// AI crew wizard
	crewAI := NewCrewAIHandler(r.db, r.logger)
	r.mux.Handle("POST /api/v1/crew-ai-suggest", authed(wsCtx(http.HandlerFunc(crewAI.Suggest))))

	// Missions
	var missionEngineForPublic *orchestrator.MissionEngine
	if r.missionCallback != nil {
		if mc, ok := r.missionCallback.(*orchestrator.MissionEngine); ok {
			missionEngineForPublic = mc
		}
	}
	missions := NewMissionHandler(r.db, r.hub, missionEngineForPublic, r.logger)
	r.mux.Handle("GET /api/v1/missions", authed(wsCtx(http.HandlerFunc(missions.ListAll))))
	r.mux.Handle("GET /api/v1/mission-metrics", authed(wsCtx(http.HandlerFunc(missions.Metrics))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions", authed(wsCtx(http.HandlerFunc(missions.List))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions", authed(wsCtx(http.HandlerFunc(missions.Create))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Get))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Update))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Delete))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/start", authed(wsCtx(http.HandlerFunc(missions.Start))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/restart", authed(wsCtx(http.HandlerFunc(missions.Restart))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/resume", authed(wsCtx(http.HandlerFunc(missions.Resume))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/clone", authed(wsCtx(http.HandlerFunc(missions.Clone))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/tasks", authed(wsCtx(http.HandlerFunc(missions.CreateTask))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}", authed(wsCtx(http.HandlerFunc(missions.UpdateTask))))

	// Mission Proposals (workspace-scoped)
	proposals := NewProposalHandler(r.db, r.hub, missionEngineForPublic, r.logger)
	r.mux.Handle("GET /api/v1/mission-proposals", authed(wsCtx(http.HandlerFunc(proposals.List))))
	r.mux.Handle("POST /api/v1/mission-proposals", authed(wsCtx(http.HandlerFunc(proposals.Create))))
	r.mux.Handle("GET /api/v1/mission-proposals/{proposalId}", authed(wsCtx(http.HandlerFunc(proposals.Get))))
	r.mux.Handle("POST /api/v1/mission-proposals/{proposalId}/approve", authed(wsCtx(http.HandlerFunc(proposals.Approve))))
	r.mux.Handle("POST /api/v1/mission-proposals/{proposalId}/reject", authed(wsCtx(http.HandlerFunc(proposals.Reject))))
	r.mux.Handle("DELETE /api/v1/mission-proposals/{proposalId}", authed(wsCtx(http.HandlerFunc(proposals.Delete))))

	// Agents (require workspace context)
	r.mux.Handle("GET /api/v1/agents/fleet-status", authed(wsCtx(http.HandlerFunc(agents.FleetStatus))))
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
	r.mux.Handle("POST /api/v1/credentials/test", authed(http.HandlerFunc(creds.Test)))
	r.mux.Handle("GET /api/v1/credentials/default-env-var", authed(http.HandlerFunc(creds.DefaultEnvVar)))
	r.mux.Handle("GET /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Get))))
	r.mux.Handle("PATCH /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Update))))
	r.mux.Handle("PUT /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Update))))
	r.mux.Handle("DELETE /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Delete))))

	// Skills (require auth)
	r.mux.Handle("GET /api/v1/skills", authed(wsCtx(http.HandlerFunc(skills.List))))
	r.mux.Handle("GET /api/v1/skills/{skillId}", authed(wsCtx(http.HandlerFunc(skills.Get))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/skills/import", authed(wsCtx(http.HandlerFunc(skills.Import))))

	// Runs (require workspace context)
	r.mux.Handle("GET /api/v1/runs", authed(wsCtx(http.HandlerFunc(runs.List))))

	// Audit logs (require workspace context + manage role)
	r.mux.Handle("GET /api/v1/audit", authed(wsCtx(http.HandlerFunc(audit.List))))

	// MCP tool call audit (require workspace context)
	mcpAudit := NewMCPAuditHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/mcp-tool-calls", authed(wsCtx(http.HandlerFunc(mcpAudit.List))))

	// OAuth flow (auth required for initiate, callback is unauthenticated — uses state token)
	oauth := NewOAuthHandler(r.db, r.logger)
	if r.hub != nil {
		oauth.SetHub(r.hub)
	}
	r.mux.Handle("GET /api/v1/oauth/providers", authed(http.HandlerFunc(oauth.ListProviders)))
	r.mux.Handle("POST /api/v1/oauth/initiate", authed(wsCtx(http.HandlerFunc(oauth.Initiate))))
	r.mux.HandleFunc("GET /api/v1/oauth/callback", oauth.Callback) // No auth — uses state token
	r.mux.Handle("POST /api/v1/oauth/exchange", authed(wsCtx(http.HandlerFunc(oauth.Exchange))))
	r.mux.Handle("POST /api/v1/oauth/loopback", authed(wsCtx(http.HandlerFunc(oauth.Loopback))))
	r.mux.Handle("POST /api/v1/oauth/discover", authed(http.HandlerFunc(oauth.Discover)))
	r.mux.Handle("POST /api/v1/oauth/auto-connect", authed(wsCtx(http.HandlerFunc(oauth.AutoConnect))))

	// Captain (require auth + workspace context)
	captain := NewCaptainHandler(r.db, r.logger)
	if r.captainLLM != nil {
		captain.SetProvider(r.captainLLM)
	}
	if r.captainMissionEngine != nil {
		captain.SetMissionEngine(r.captainMissionEngine)
	}
	r.mux.Handle("POST /api/v1/captain/chat", authed(wsCtx(http.HandlerFunc(captain.Chat))))
	r.mux.Handle("GET /api/v1/captain/context", authed(wsCtx(http.HandlerFunc(captain.Context))))
	r.mux.Handle("GET /api/v1/captain/history", authed(wsCtx(http.HandlerFunc(captain.History))))
	r.mux.Handle("DELETE /api/v1/captain/history", authed(wsCtx(http.HandlerFunc(captain.ClearHistory))))

	// Onboarding (require auth, no workspace context needed)
	onboarding := NewOnboardingHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/onboarding/status", authed(http.HandlerFunc(onboarding.Status)))
	r.mux.Handle("POST /api/v1/onboarding/complete", authed(http.HandlerFunc(onboarding.Complete)))
	r.mux.Handle("POST /api/v1/onboarding/setup", authed(http.HandlerFunc(onboarding.Setup)))

	// Auth (no auth required)
	authH := NewAuthHandler(r.db, r.logger, r.authMw.validator, r.allowSignup)
	r.mux.HandleFunc("POST /api/v1/bootstrap", authH.Bootstrap)
	r.mux.HandleFunc("POST /api/v1/auth/signup", authH.Signup)
	r.mux.Handle("GET /api/v1/ws-token", authed(http.HandlerFunc(authH.WsToken)))

	// CLI token management (auth required)
	cliTokenH := NewCLITokenHandler(r.db, r.logger)
	r.mux.Handle("POST /api/v1/auth/cli-token", authed(http.HandlerFunc(cliTokenH.Create)))
	r.mux.Handle("GET /api/v1/auth/cli-token/validate", authed(http.HandlerFunc(cliTokenH.Validate)))
	r.mux.Handle("GET /api/v1/auth/cli-tokens", authed(http.HandlerFunc(cliTokenH.List)))
	r.mux.Handle("DELETE /api/v1/auth/cli-tokens/{tokenId}", authed(http.HandlerFunc(cliTokenH.Revoke)))

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
	r.mux.Handle("PUT /api/v1/agents/{agentId}/files/save", authed(wsCtx(http.HandlerFunc(proxy.AgentFileSave))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/files", authed(wsCtx(http.HandlerFunc(proxy.CrewFiles))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/files/download", authed(wsCtx(http.HandlerFunc(proxy.CrewFileDownload))))
	r.mux.Handle("PUT /api/v1/crews/{crewId}/files/save", authed(wsCtx(http.HandlerFunc(proxy.CrewFileSave))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/logs", authed(wsCtx(http.HandlerFunc(proxy.AgentLogs))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/stop", authed(wsCtx(http.HandlerFunc(proxy.AgentStop))))
	r.mux.Handle("GET /api/v1/chats/{chatId}/messages", authed(http.HandlerFunc(proxy.ChatMessages)))

	// Internal routes (for crewshipd IPC, X-Internal-Token auth)
	internal := NewInternalHandler(r.db, r.internalToken, r.logger)
	if r.hub != nil {
		internal.SetHub(r.hub)
	}
	if r.keeperConfig != nil && r.keeperConfig.Enabled {
		internal.SetKeeperEnabled(true)
	}
	internalAuth := internal.requireInternal
	r.mux.Handle("GET /api/v1/internal/credentials", internalAuth(http.HandlerFunc(internal.ListCredentials)))
	r.mux.Handle("PATCH /api/v1/internal/credentials/{credentialId}", internalAuth(http.HandlerFunc(internal.UpdateCredentialStatus)))
	r.mux.Handle("POST /api/v1/internal/chats", internalAuth(http.HandlerFunc(internal.CreateChat)))
	r.mux.Handle("GET /api/v1/internal/chats/{chatId}/resolve", internalAuth(http.HandlerFunc(internal.ResolveChat)))
	r.mux.Handle("GET /api/v1/internal/agents/{agentId}/resolve", internalAuth(http.HandlerFunc(internal.ResolveAgent)))
	r.mux.Handle("GET /api/v1/internal/agents/{agentId}/webhook-secret", internalAuth(http.HandlerFunc(internal.GetWebhookSecret)))
	r.mux.Handle("POST /api/v1/internal/runs", internalAuth(http.HandlerFunc(internal.CreateRun)))
	r.mux.Handle("PATCH /api/v1/internal/runs/{runId}", internalAuth(http.HandlerFunc(internal.UpdateRun)))
	r.mux.Handle("PATCH /api/v1/internal/chats/{chatId}/message-count", internalAuth(http.HandlerFunc(internal.IncrementMessageCount)))
	r.mux.Handle("PATCH /api/v1/internal/chats/{chatId}/title", internalAuth(http.HandlerFunc(internal.UpdateChatTitle)))
	r.mux.Handle("GET /api/v1/internal/crews", internalAuth(http.HandlerFunc(internal.ListCrews)))
	r.mux.Handle("POST /api/v1/internal/crews", internalAuth(http.HandlerFunc(internal.CreateCrew)))
	r.mux.Handle("POST /api/v1/internal/agents", internalAuth(http.HandlerFunc(internal.CreateAgent)))
	r.mux.Handle("GET /api/v1/internal/crew-connections", internalAuth(http.HandlerFunc(internal.ListCrewConnections)))
	r.mux.Handle("POST /api/v1/internal/mcp-tool-calls", internalAuth(http.HandlerFunc(internal.RecordMCPToolCall)))

	// Assignment routes (internal auth, called by sidecar on behalf of lead agents)
	assign := NewAssignmentHandler(r.db, r.orch, r.hub, r.internalToken, r.logger)
	if r.missionCallback != nil {
		assign.SetMissionCallback(r.missionCallback)
		// Wire AssignmentHandler as the TaskDispatcher so the MissionEngine
		// can dispatch tasks (including cross-crew) through the same code path.
		if me, ok := r.missionCallback.(*orchestrator.MissionEngine); ok {
			me.SetDispatcher(assign)
		}
	}
	r.mux.Handle("POST /api/v1/internal/assignments", internalAuth(http.HandlerFunc(assign.Create)))
	r.mux.Handle("GET /api/v1/internal/assignments/{assignmentId}", internalAuth(http.HandlerFunc(assign.Get)))

	// Internal mission routes (called by sidecar on behalf of lead agents)
	var missionEngineForInternal *orchestrator.MissionEngine
	if mc, ok := r.missionCallback.(*orchestrator.MissionEngine); ok {
		missionEngineForInternal = mc
	}
	internalMissions := NewInternalMissionHandler(r.db, r.hub, missionEngineForInternal, r.logger)
	r.mux.Handle("POST /api/v1/internal/missions", internalAuth(http.HandlerFunc(internalMissions.Create)))
	r.mux.Handle("GET /api/v1/internal/missions/{missionId}", internalAuth(http.HandlerFunc(internalMissions.Get)))
	r.mux.Handle("POST /api/v1/internal/missions/{missionId}/start", internalAuth(http.HandlerFunc(internalMissions.Start)))

	// Internal mission proposal routes (called by sidecar on behalf of COORDINATOR agents)
	internalProposals := NewProposalHandler(r.db, r.hub, missionEngineForInternal, r.logger)
	r.mux.Handle("GET /api/v1/internal/mission-proposals", internalAuth(internalWsCtx(http.HandlerFunc(internalProposals.List))))
	r.mux.Handle("POST /api/v1/internal/mission-proposals", internalAuth(internalWsCtx(http.HandlerFunc(internalProposals.Create))))
	r.mux.Handle("GET /api/v1/internal/mission-proposals/{proposalId}", internalAuth(internalWsCtx(http.HandlerFunc(internalProposals.Get))))

	// Crew assignments (public, authenticated)
	r.mux.Handle("GET /api/v1/crews/{crewId}/assignments", authed(wsCtx(http.HandlerFunc(assign.List))))

	// Query routes (peer-to-peer communication, standup summaries, escalations)
	queries := NewQueryHandler(r.db, r.orch, r.hub, r.internalToken, r.logger)
	r.mux.Handle("POST /api/v1/internal/queries", internalAuth(http.HandlerFunc(queries.Create)))
	r.mux.Handle("GET /api/v1/internal/standup", internalAuth(http.HandlerFunc(queries.Standup)))
	r.mux.Handle("POST /api/v1/internal/escalations", internalAuth(http.HandlerFunc(queries.CreateEscalation)))
	r.mux.Handle("GET /api/v1/internal/escalations/{escalationId}/wait", internalAuth(http.HandlerFunc(queries.WaitForEscalationResponse)))

	// Crew peer conversations, standup, and escalations (public, authenticated)
	r.mux.Handle("GET /api/v1/crews/{crewId}/peer-conversations", authed(wsCtx(http.HandlerFunc(queries.ListPeerConversations))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/standup", authed(wsCtx(http.HandlerFunc(queries.Standup))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/escalations", authed(wsCtx(http.HandlerFunc(queries.ListEscalations))))
	r.mux.Handle("PATCH /api/v1/escalations/{escalationId}/resolve", authed(wsCtx(http.HandlerFunc(queries.ResolveEscalation))))

	// Workspace-wide escalation count (public, authenticated)
	r.mux.Handle("GET /api/v1/escalations/pending-count", authed(wsCtx(http.HandlerFunc(queries.PendingEscalationCount))))

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

	// Webhooks (public, HMAC-secret protected)
	if r.orch != nil && r.keeperContainer != nil && r.logWriter != nil && r.internalToken != "" {
		// Use IPC resolver to talk to our own internal endpoints
		baseURL := r.internalBaseURL
		if baseURL == "" {
			baseURL = "http://localhost:8080"
		}
		resolver := chatbridge.NewIPCResolver(baseURL, r.internalToken, r.logger)
		wh := NewWebhookHandler(r.logger, resolver, r.orch, r.hub, r.keeperContainer, r.logWriter)
		r.mux.Handle("POST /api/v1/webhooks/{crewId}/{agentId}/trigger", http.HandlerFunc(wh.ServeHTTP))
	}
}
