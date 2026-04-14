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

var Crews = []CrewDef{
	{
		Name: "Engineering", Slug: "engineering",
		Color: "#3B82F6", Icon: "terminal",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{"ghcr.io/devcontainers/features/common-utils:2":{},"ghcr.io/devcontainers/features/github-cli:1":{}}}`,
		MiseConfig:         `{"tools":{"node":"22","python":"3.12"}}`,
	},
	{
		Name: "Quality", Slug: "quality",
		Color: "#10B981", Icon: "shield-check",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{"ghcr.io/devcontainers/features/common-utils:2":{},"ghcr.io/devcontainers/features/python:1":{}}}`,
	},
	{
		Name: "DevOps", Slug: "devops",
		Color: "#EF4444", Icon: "server",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{"ghcr.io/devcontainers/features/common-utils:2":{},"ghcr.io/devcontainers/features/docker-in-docker:2":{},"ghcr.io/devcontainers/features/kubectl-helm-minikube:1":{}}}`,
		MiseConfig:         `{"tools":{"terraform":"1.9"}}`,
	},
	{
		Name: "Research", Slug: "research",
		Color: "#06B6D4", Icon: "telescope",
		RuntimeImage:       "debian:bookworm-slim",
		DevcontainerConfig: `{"image":"debian:bookworm-slim","features":{"ghcr.io/devcontainers/features/common-utils:2":{},"ghcr.io/devcontainers/features/python:1":{}}}`,
		MiseConfig:         `{"tools":{"python":"3.12"}}`,
	},
}
