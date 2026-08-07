package main

// This file is the declaration half of the gate. scan.go derives what the tree
// actually reaches; everything below states what we say it reaches. main.go
// diffs the two and fails on any difference in either direction — a new Docker
// call that nobody listed, and an entry that no longer has a call site.
//
// The published deployment is docs/guides/docker-socket-proxy.mdx and
// docker/docker-socket-proxy.yml. Both are checked against this table, so
// changing the code without changing the docs is a red build rather than a
// silently wrong security guide.

// localMethods are SDK methods that issue no HTTP request: they read or mutate
// client-side state only. They need no proxy permission and are excluded from
// the surface. Close() is the reason this list exists at all — it collides with
// io.Closer, which appears thousands of times.
// TestExclusionListsNameRealSDKMethods asserts each name below really is an SDK
// method, so an entry cannot quietly become a no-op after a moby upgrade.
var localMethods = map[string]bool{
	"Close":         true,
	"ClientVersion": true,
	"DaemonHost":    true,
	"Dialer":        true,
}

// ambiguousMethods are SDK method names that also name methods on types we use
// constantly: slog.Logger.Info, fs.DirEntry.Info, sql.DB.Ping. A call to one of
// these counts as a Docker call only when its receiver is named in
// dockerReceivers. Every other SDK method is counted unconditionally.
var ambiguousMethods = map[string]bool{
	"Info":   true,
	"Ping":   true,
	"Events": true,
}

// dockerReceivers are the identifiers that hold a Docker client in this tree,
// used only to disambiguate the names above. Reviewed data: adding one here is
// a claim that the value really is an SDK client.
var dockerReceivers = map[string]bool{
	"cli":          true, // internal/provider/docker free functions
	"client":       true, // Provider.client
	"Client":       true, // backup.MobyDockerOps.Client
	"docker":       true, // devcontainer.Provisioner.docker, api handlers
	"dockerClient": true, // api.Router.dockerClient
	"gcClient":     true, // api.ProvisioningHandler.gcClient
	"dc":           true, // locals in internal/api/router_orchestration.go
	"dcl":          true,
}

// Tier names which deployment profile an endpoint belongs to. The split is not
// cosmetic: the core profile is what an agent runtime needs, and it is the one
// an operator can adopt without giving the proxy image-mutating verbs.
const (
	// TierCore is required for any working instance: creating, running,
	// exec-ing into and cleaning up agent containers.
	TierCore = "core"
	// TierDevcontainer is required only when crews carry a devcontainer or
	// mise config. internal/api/crew_runtime_config.go:crewNeedsProvision
	// decides that per crew; instances with neither can leave it off.
	TierDevcontainer = "devcontainer"
)

// Endpoint is one Docker Engine API operation we reach, with the proxy
// permissions it needs and the packages allowed to reach it.
type Endpoint struct {
	Method    string   // Docker SDK method name
	HTTP      string   // verb and path on the Engine API
	ProxyVars []string // Tecnativa docker-socket-proxy variables that must be 1
	Tier      string
	Packages  []string // packages with a call site, repo-relative
	Why       string
}

// proxyVarPOST gates every verb HAProxy does not match with METH_GET. That is
// POST, PUT, DELETE *and* HEAD — CopyToContainer and Ping both issue a HEAD,
// and both are denied when POST is 0.
const proxyVarPOST = "POST"

// allowList is the full derived surface. Sorted by method; main.go asserts the
// derivation matches it exactly.
var allowList = []Endpoint{
	{
		Method: "ContainerCommit", HTTP: "POST /commit", ProxyVars: []string{"COMMIT", proxyVarPOST},
		Tier: TierDevcontainer, Packages: []string{"internal/devcontainer"},
		Why: "final step of crew provisioning: bakes the provisioned container into a cached image",
	},
	{
		Method: "ContainerCreate", HTTP: "POST /containers/create", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/devcontainer", "internal/provider/docker"},
		Why: "creates every agent, sidecar and provisioning container",
	},
	{
		Method: "ContainerInspect", HTTP: "GET /containers/{id}/json", ProxyVars: []string{"CONTAINERS"},
		Tier: TierCore, Packages: []string{"internal/api", "internal/backup", "internal/provider/docker"},
		Why: "state, mounts and network reads for running crews",
	},
	{
		Method: "ContainerList", HTTP: "GET /containers/json", ProxyVars: []string{"CONTAINERS"},
		Tier: TierCore, Packages: []string{"internal/api", "internal/provider/docker"},
		Why: "reconciles the crew inventory against what is actually running",
	},
	{
		Method: "ContainerPause", HTTP: "POST /containers/{id}/pause", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/backup"},
		Why: "quiesces a crew so a backup captures a consistent filesystem",
	},
	{
		Method: "ContainerRemove", HTTP: "DELETE /containers/{id}", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/api", "internal/devcontainer", "internal/provider/docker"},
		Why: "tears down crews, temp provisioning containers and stranded runtime",
	},
	{
		Method: "ContainerStart", HTTP: "POST /containers/{id}/start", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/devcontainer", "internal/provider/docker"},
		Why: "starts agent, sidecar and provisioning containers",
	},
	{
		Method: "ContainerStats", HTTP: "GET /containers/{id}/stats", ProxyVars: []string{"CONTAINERS"},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "memory and CPU readings behind the admission gate",
	},
	{
		Method: "ContainerStop", HTTP: "POST /containers/{id}/stop", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/devcontainer", "internal/provider/docker"},
		Why: "stops crews and sidecars before removal",
	},
	{
		Method: "ContainerUnpause", HTTP: "POST /containers/{id}/unpause", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/backup"},
		Why: "resumes a crew after the backup snapshot",
	},
	{
		Method: "ContainerWait", HTTP: "POST /containers/{id}/wait", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "blocks on one-shot helper containers (secrets sweep, image cache warm)",
	},
	{
		Method: "CopyFromContainer", HTTP: "GET /containers/{id}/archive", ProxyVars: []string{"CONTAINERS"},
		Tier: TierCore, Packages: []string{"internal/backup"},
		Why: "streams crew content out for a backup",
	},
	{
		Method: "CopyToContainer", HTTP: "HEAD + PUT /containers/{id}/archive", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/backup", "internal/devcontainer", "internal/provider/docker"},
		Why: "writes devcontainer features, restored backups and the sidecar binary into a container",
	},
	{
		Method: "ExecAttach", HTTP: "POST /exec/{id}/start (hijacked)", ProxyVars: []string{"EXEC", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/backup", "internal/devcontainer", "internal/provider/docker"},
		Why: "streams stdio for an agent command",
	},
	{
		Method: "ExecCreate", HTTP: "POST /containers/{id}/exec", ProxyVars: []string{"CONTAINERS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/backup", "internal/devcontainer", "internal/provider/docker"},
		Why: "creates the exec session an agent turn runs in; note the path is under /containers, not /exec",
	},
	{
		Method: "ExecInspect", HTTP: "GET /exec/{id}/json", ProxyVars: []string{"EXEC"},
		Tier: TierCore, Packages: []string{"internal/api", "internal/backup", "internal/devcontainer", "internal/provider/docker"},
		Why: "reads the exit code of a finished exec",
	},
	{
		Method: "ExecResize", HTTP: "POST /exec/{id}/resize", ProxyVars: []string{"EXEC", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "sizes the TTY for interactive CLI adapters",
	},
	{
		Method: "ExecStart", HTTP: "POST /exec/{id}/start", ProxyVars: []string{"EXEC", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "runs a detached exec",
	},
	{
		Method: "ImageInspect", HTTP: "GET /images/{name}/json", ProxyVars: []string{"IMAGES"},
		Tier: TierCore, Packages: []string{"internal/api", "internal/devcontainer", "internal/provider/docker"},
		Why: "checks an image is present locally and reads its baked-in env",
	},
	{
		Method: "ImageList", HTTP: "GET /images/json", ProxyVars: []string{"IMAGES"},
		Tier: TierCore, Packages: []string{"internal/api", "internal/devcontainer"},
		Why: "finds cached provisioning images for reuse and garbage collection",
	},
	{
		Method: "ImagePull", HTTP: "POST /images/create", ProxyVars: []string{"IMAGES", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/devcontainer", "internal/provider/docker"},
		Why: "pulls the base image for a crew and the sidecar image",
	},
	{
		Method: "ImageRemove", HTTP: "DELETE /images/{name}", ProxyVars: []string{"IMAGES", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/api"},
		Why: "garbage-collects unreferenced crewship-cache:* images",
	},
	{
		Method: "ImageTag", HTTP: "POST /images/{name}/tag", ProxyVars: []string{"IMAGES", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "restores the local tag after a digest-pinned pull (#1825) — `docker pull repo@sha256:…` fetches the manifest but leaves the image unnamed, and everything downstream still addresses it by tag",
	},
	{
		Method: "Info", HTTP: "GET /info", ProxyVars: []string{"INFO"},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "detects the cgroup version and whether the daemon shares the host filesystem",
	},
	{
		Method: "NetworkCreate", HTTP: "POST /networks/create", ProxyVars: []string{"NETWORKS", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "creates the per-crew internal network",
	},
	{
		Method: "NetworkList", HTTP: "GET /networks", ProxyVars: []string{"NETWORKS"},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "checks whether the crew network already exists",
	},
	{
		Method: "Ping", HTTP: "HEAD /_ping (GET on fallback)", ProxyVars: []string{"PING"},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "daemon reachability probe during socket detection",
	},
	{
		Method: "ServerVersion", HTTP: "GET /version", ProxyVars: []string{"VERSION"},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "distinguishes dockerd from a nerdctl-style shim during detection",
	},
	{
		Method: "VolumeCreate", HTTP: "POST /volumes/create", ProxyVars: []string{"VOLUMES", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "creates the per-agent home and memory volumes",
	},
	{
		Method: "VolumeInspect", HTTP: "GET /volumes/{name}", ProxyVars: []string{"VOLUMES"},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "reads a volume's mountpoint and labels",
	},
	{
		Method: "VolumeList", HTTP: "GET /volumes", ProxyVars: []string{"VOLUMES"},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "enumerates crew volumes for reconciliation and pruning",
	},
	{
		Method: "VolumeRemove", HTTP: "DELETE /volumes/{name}", ProxyVars: []string{"VOLUMES", proxyVarPOST},
		Tier: TierCore, Packages: []string{"internal/provider/docker"},
		Why: "removes volumes belonging to a deleted crew",
	},
}

// declaredShellouts are the places that reach the daemon by executing a
// docker-compatible CLI with DOCKER_HOST pinned, rather than through the SDK.
// They matter because no amount of Go type-checking would catch them: they show
// up as a 403 mid-build, at run time, on an operator's instance.
var declaredShellouts = []DeclaredShellout{
	{
		File: "internal/devcontainer/imagebuilder.go",
		HTTP: "POST /build, POST /session",
		// BuildKit opens a /session for filesync alongside /build. Newer
		// daemons can route parts of that over /grpc; we have not driven this
		// path behind a proxy, and the docs say so rather than implying we did.
		ProxyVars: []string{"BUILD", "SESSION", proxyVarPOST},
		Tier:      TierDevcontainer,
		Why:       "`docker build` with DOCKER_BUILDKIT=1 for crews that declare devcontainer features",
	},
}

// DeclaredShellout is the declaration counterpart of Shellout.
type DeclaredShellout struct {
	File      string
	HTTP      string
	ProxyVars []string
	Tier      string
	Why       string
}

// declaredPackages is every package permitted to import the Docker SDK client.
// Widening this is widening the blast radius of a compromised handler, so it is
// a deliberate, reviewed act rather than an import someone adds in passing.
var declaredPackages = []string{
	"internal/api",
	"internal/backup",
	"internal/devcontainer",
	"internal/provider/docker",
	// The gate itself: imports the SDK only to read *client.Client's method set
	// by reflection. It issues no requests.
	"scripts/docker-api-surface",
}

// composePath is the supported deployment, checked against this table.
// composeService is the proxy service inside it; only that service's
// environment block is parsed, so an unrelated `FOO: 1` elsewhere in the file
// cannot be mistaken for a granted permission.
const (
	composePath    = "docker/docker-compose.prod.yml"
	composeService = "docker-socket-proxy"
)

// docsPath is the published guide, checked against this table.
const docsPath = "docs/guides/docker-socket-proxy.mdx"
