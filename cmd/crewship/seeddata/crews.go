package seeddata

// CrewDef defines a crew to seed.
type CrewDef struct {
	Name               string
	Slug               string
	Color              string
	Icon               string
	RuntimeImage       string
	DevcontainerConfig string
	MiseConfig         string
}

// Default features included in every demo crew.
// Versions pinned intentionally — latest floating versions risk breaking Crewship
// when upstream ships format/flag changes. Bump after matrix-testing on dev.
//   - common-utils: creates the `agent` user (UID 1001) + /home/agent.
//     Replaces our former Go EnsureAgentUser helper.
//   - claude-code (devcontainers-extra): installs the Claude Code CLI globally.
//     Replaces our former Go EnsureClaudeCode helper.
const baseFeatures = `"ghcr.io/devcontainers/features/common-utils:2":{"username":"agent","userUid":"1001","userGid":"1001","installZsh":false,"upgradePackages":false},"ghcr.io/devcontainers-extra/features/claude-code:2":{}`

// seedBaseImage matches the recommended preset in components/features/crews/runtime-config.tsx.
// Node 22 base speeds provisioning (claude-code feature skips Node install).
const seedBaseImage = "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm"

var Crews = []CrewDef{
	{
		Name: "Engineering", Slug: "engineering",
		Color: "#3B82F6", Icon: "terminal",
		RuntimeImage:       seedBaseImage,
		DevcontainerConfig: `{"image":"` + seedBaseImage + `","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/github-cli:1":{}}}`,
		MiseConfig:         `{"tools":{"node":"22","python":"3.12"}}`,
	},
	{
		Name: "Quality", Slug: "quality",
		Color: "#10B981", Icon: "shield-check",
		RuntimeImage:       seedBaseImage,
		DevcontainerConfig: `{"image":"` + seedBaseImage + `","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/python:1":{}}}`,
	},
	{
		Name: "DevOps", Slug: "devops",
		Color: "#EF4444", Icon: "server",
		RuntimeImage:       seedBaseImage,
		DevcontainerConfig: `{"image":"` + seedBaseImage + `","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/docker-in-docker:2":{},"ghcr.io/devcontainers/features/kubectl-helm-minikube:1":{}}}`,
		MiseConfig:         `{"tools":{"terraform":"1.9"}}`,
	},
	{
		Name: "Research", Slug: "research",
		Color: "#06B6D4", Icon: "telescope",
		RuntimeImage:       seedBaseImage,
		DevcontainerConfig: `{"image":"` + seedBaseImage + `","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/python:1":{}}}`,
		MiseConfig:         `{"tools":{"python":"3.12"}}`,
	},
}
