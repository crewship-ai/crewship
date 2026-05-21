package api

// RouterOption helpers extracted from router.go for readability —
// the 30+ functional-options form a self-contained chunk that grows
// every time a new dependency is wired in. Keeping them here means
// `router.go` stays focused on Router lifecycle + dispatch.

import (
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
	dockerclient "github.com/docker/docker/client"
)

type RouterOption func(*Router)

// WithSocketPath sets the Unix socket path used for IPC with the sidecar.
func WithSocketPath(path string) RouterOption {
	return func(r *Router) {
		r.socketPath = path
	}
}

// WithInternalToken sets the shared secret used to authenticate internal API calls from the sidecar.
func WithInternalToken(token string) RouterOption {
	return func(r *Router) {
		r.internalToken = token
	}
}

// WithInternalBaseURL sets the base URL for internal API calls from the backend to itself.
func WithInternalBaseURL(url string) RouterOption {
	return func(r *Router) {
		r.internalBaseURL = url
	}
}

// WithPortExposePublicURL sets the base URL used when handing capability URLs
// back to agents for exposed container ports. Should be the externally
// reachable origin of this crewshipd (e.g. "http://crewship.example.com:8080").
// If unset, the handler falls back to "http://localhost:8080" which is fine
// for unit tests but not reachable from a user's browser.
func WithPortExposePublicURL(u string) RouterOption {
	return func(r *Router) {
		r.portExposePublicURL = u
	}
}

// WithPortExposeNetwork overrides the Docker bridge name the port-expose
// handler probes for the agent container's IP. The default
// ("crewship-agents") only matches single-instance dev deployments;
// multi-instance setups (dev.sh) suffix the network with the instance
// number ("crewship-1-agents"), and a misconfigured deployment otherwise
// silently 502s every /expose-port call with "container not reachable
// on crew network". Should match cfg.Container.Network.
func WithPortExposeNetwork(name string) RouterOption {
	return func(r *Router) {
		r.portExposeNetwork = name
	}
}

// WithHub attaches a WebSocket hub for real-time event broadcasting to connected clients.
func WithHub(hub *ws.Hub) RouterOption {
	return func(r *Router) {
		r.hub = hub
	}
}

// WithOrchestrator attaches the container orchestrator used to run agent assignments.
func WithOrchestrator(orch *orchestrator.Orchestrator) RouterOption {
	return func(r *Router) {
		r.orch = orch
	}
}

// WithKeeperGatekeeper attaches the Keeper gatekeeper policy evaluator.
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

// WithGoogleOAuth configures the Google OAuth client credentials and base URL
// used by the NextAuth-compatible auth routes.
func WithGoogleOAuth(clientID, secret, baseURL string) RouterOption {
	return func(r *Router) {
		r.googleClientID = clientID
		r.googleSecret = secret
		r.authBaseURL = baseURL
	}
}

// WithStoragePath sets the base filesystem path for crew file storage.
func WithStoragePath(path string) RouterOption {
	return func(r *Router) {
		r.storagePath = path
	}
}

// WithCatalogFetcher wires the dynamic devcontainer feature catalog fetcher.
func WithCatalogFetcher(f *devcontainer.CatalogFetcher) RouterOption {
	return func(r *Router) {
		r.catalogFetcher = f
	}
}

// WithRuntimeFetcher wires the dynamic mise runtime catalog fetcher.
func WithRuntimeFetcher(f *devcontainer.RuntimeFetcher) RouterOption {
	return func(r *Router) {
		r.runtimeFetcher = f
	}
}

// WithDockerClient attaches a Docker SDK client used by the devcontainer
// provisioner (image commits, temp containers). If unset, the provision
// trigger endpoint returns 503.
func WithDockerClient(c *dockerclient.Client) RouterOption {
	return func(r *Router) {
		r.dockerClient = c
	}
}

// WithFeatureCacheDir sets the on-disk cache directory for downloaded
// devcontainer feature tarballs.
func WithFeatureCacheDir(path string) RouterOption {
	return func(r *Router) {
		r.featureCacheDir = path
	}
}

// WithKeeperConversations attaches a conversation reader so Keeper can inspect agent chat history.
func WithKeeperConversations(cr ConversationReader) RouterOption {
	return func(r *Router) {
		r.keeperConvReader = cr
	}
}

// WithMissionCallback attaches a callback invoked when assignment results affect missions.
func WithMissionCallback(cb MissionCallback) RouterOption {
	return func(r *Router) {
		r.missionCallback = cb
	}
}

// WithLogWriter attaches a log collector writer for structured log ingestion from agents.
func WithLogWriter(lw *logcollector.Writer) RouterOption {
	return func(r *Router) {
		r.logWriter = lw
	}
}

// WithJournal wires the Crew Journal emitter used by all handlers to log
// structured events. When unset, Router.Journal() returns a no-op so code
// can emit unconditionally without nil-checking.
func WithJournal(j journal.Emitter) RouterOption {
	return func(r *Router) {
		r.journal = j
	}
}

// WithLicense attaches the license for enforcing feature gates and seat limits.
func WithLicense(lic *license.License) RouterOption {
	return func(r *Router) {
		r.license = lic
	}
}

// WithConsolidator wires the shared consolidate.Consolidator so the
// manual /api/v1/consolidate/run endpoint can re-use the same
// summarizer + logger the background runner does. Unset → the endpoint
// returns 503 (feature not configured).
func WithConsolidator(c *consolidate.Consolidator) RouterOption {
	return func(r *Router) {
		r.consolidator = c
	}
}

// WithConsolidateMemoryRoot sets the parent directory manual consolidation
// runs write learned-*.md into. Should match consolidate.RunnerOptions.
// CrewMemoryRoot so scheduled + manual runs share an output tree.
func WithConsolidateMemoryRoot(path string) RouterOption {
	return func(r *Router) {
		r.consolidateMemoryRoot = path
	}
}

// WithMemoryVersionsBlobRoot sets the content-addressed blob root the
// v90 memory_versions audit trail writes under. Forwarded to the
// ProposedHandler so ApproveProposal records a row + blob on every
// successful canonical merge. Empty (or unconfigured) disables
// versioning on the approve path silently — the approve itself still
// succeeds, just without the EU AI Act Art. 14 audit row.
func WithMemoryVersionsBlobRoot(path string) RouterOption {
	return func(r *Router) {
		r.memoryVersionsBlobRoot = path
	}
}

// WithHybridSearchEmbedder feeds the dense-vector half of the
// MemoryHybridSearchHandler. nil is fine — the handler degrades to
// FTS-only when this is missing. Production wires the same Ollama
// embedder the episodic recall adapter uses, so a single embedder
// instance covers both surfaces.
func WithHybridSearchEmbedder(e episodic.Embedder) RouterOption {
	return func(r *Router) {
		r.hybridSearchEmbedder = e
	}
}

// WithHybridSearchProvider feeds the FTS half of the hybrid
// search handler. nil is fine — handler degrades to episodic-only.
// Production wires an adapter around *memory.WorkspaceMemoryRegistry.
func WithHybridSearchProvider(p WorkspaceMemoryProvider) RouterOption {
	return func(r *Router) {
		r.hybridSearchProvider = p
	}
}

// WithAuxiliaryModels carries the PR-B F3 per-slot aux-model
// assignment into the Router so the system aux-status endpoint and
// future PR-C evaluators can resolve the right provider/model/timeout
// for each subsystem. The auxModelsSet flag is what AuxModels()
// inspects to decide whether to return the wired config or the
// llm.DefaultAuxiliaryModels MVP fallback — passing a deliberately
// empty AuxiliaryModels{} therefore still counts as "configured" and
// will surface as "unconfigured" rows in the status response (which
// is the loud-error behaviour PR-Z Z.2 calls for).
func WithAuxiliaryModels(cfg llm.AuxiliaryModels) RouterOption {
	return func(r *Router) {
		r.auxModels = cfg
		r.auxModelsSet = true
	}
}

// ServeHTTP dispatches incoming requests to the registered route handlers.
// It applies security headers to all responses and per-IP rate limiting:
// stricter limits on auth endpoints, general limits on public API,
