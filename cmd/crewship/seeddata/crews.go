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

// Default features included in every demo crew:
//   - common-utils: creates the `agent` user (UID 1001) + /home/agent.
//     Replaces our former Go EnsureAgentUser helper.
//   - claude-code (devcontainers-extra): installs the Claude Code CLI globally.
//     Replaces our former Go EnsureClaudeCode helper.
const baseFeatures = `"ghcr.io/devcontainers/features/common-utils:2":{"username":"agent","userUid":"1001","userGid":"1001","installZsh":false},"ghcr.io/devcontainers-extra/features/claude-code:2":{}`

var Crews = []CrewDef{
	{
		Name: "Engineering", Slug: "engineering",
		Color: "#3B82F6", Icon: "terminal",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/github-cli:1":{}}}`,
		MiseConfig:         `{"tools":{"node":"22","python":"3.12"}}`,
	},
	{
		Name: "Quality", Slug: "quality",
		Color: "#10B981", Icon: "shield-check",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/python:1":{}}}`,
	},
	{
		Name: "DevOps", Slug: "devops",
		Color: "#EF4444", Icon: "server",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/docker-in-docker:2":{},"ghcr.io/devcontainers/features/kubectl-helm-minikube:1":{}}}`,
		MiseConfig:         `{"tools":{"terraform":"1.9"}}`,
	},
	{
		Name: "Research", Slug: "research",
		Color: "#06B6D4", Icon: "telescope",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{` + baseFeatures + `,"ghcr.io/devcontainers/features/python:1":{}}}`,
		MiseConfig:         `{"tools":{"python":"3.12"}}`,
	},
}
