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

// Multi-CLI install script. Run as the agent user via postCreateCommand;
// installs the five non-Claude CLIs the orchestrator dispatches (Codex,
// Gemini, OpenCode, Cursor, Factory Droid) into /home/agent/.local so a
// non-root user can write the binaries. Each tool is installed independently
// with `|| true` so a single upstream outage does not poison the whole
// provision — the missing CLI just won't be available; the orchestrator will
// surface "command not found" at first invocation.
//
//   - @openai/codex: npm wrapper that downloads the Rust binary into the
//     package's bin dir during `npm install -g`.
//   - @google/gemini-cli: pure JS, headless --output-format stream-json since
//     PR #10883.
//   - opencode-ai: sst.dev's OpenCode CLI; BYOK across providers.
//   - cursor-agent: shipped via cursor.com/install shell script (no npm
//     package). Installs into ~/.local/bin by default.
//   - droid (Factory): native binary via app.factory.ai/cli installer.
//     Requires xdg-utils on Linux (added below) — the installer otherwise
//     errors during the post-install step.
//
// Container-level apt installs (run before the npm/curl steps via the same
// postCreateCommand chain because we can't easily target two phases):
//   - xdg-utils — Droid Linux requirement
//   - ripgrep   — Cursor "rg ENOENT" safety net + faster grep tool for all CLIs
//   - python3 + python3-pip — Codex/Claude Code tool sandboxes occasionally
//     invoke Python; cheap to pre-stage
//
// The apt step uses `sudo` because the agent user is not root; common-utils
// devcontainer feature configures passwordless sudo for it.
//
// PATH is extended in containerEnv so the agent user can call the binaries
// without needing to source any shell rc file.
const baseCLIPostCreate = `(sudo apt-get update -qq && sudo apt-get install -y --no-install-recommends xdg-utils ripgrep python3 python3-pip 2>&1 | tail -3) || true && ` +
	`mkdir -p $HOME/.local && ` +
	`npm config set prefix $HOME/.local 2>/dev/null || true && ` +
	`npm install -g @openai/codex 2>&1 | tail -5 || true && ` +
	`npm install -g @google/gemini-cli 2>&1 | tail -5 || true && ` +
	`npm install -g opencode-ai 2>&1 | tail -5 || true && ` +
	`(curl -fsSL https://cursor.com/install -o /tmp/cursor-install.sh && bash /tmp/cursor-install.sh -y 2>&1 | tail -5) || true && ` +
	`(curl -fsSL https://app.factory.ai/cli -o /tmp/droid-install.sh && bash /tmp/droid-install.sh 2>&1 | tail -5) || true`

// baseContainerEnv extends PATH so the per-user install dirs from
// baseCLIPostCreate are reachable for both interactive shells and the
// orchestrator's non-shell exec calls.
const baseContainerEnv = `"containerEnv":{"PATH":"/home/agent/.local/bin:/home/agent/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}`

// seedBaseImage matches the recommended preset in components/features/crews/runtime-config.tsx.
// Node 22 base speeds provisioning (claude-code feature skips Node install).
const seedBaseImage = "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm"

// crewConfigJSON assembles the full devcontainer JSON for a crew, baking in
// the multi-CLI post-create install + PATH override. Helper exists so the per-
// crew definitions stay readable and the install string is not duplicated four
// times.
func crewConfigJSON(extraFeatures string) string {
	features := baseFeatures + extraFeatures
	return `{"image":"` + seedBaseImage + `",` +
		baseContainerEnv + `,` +
		`"features":{` + features + `},` +
		`"postCreateCommand":"` + baseCLIPostCreate + `"}`
}

var Crews = []CrewDef{
	{
		// Engineering: backend/frontend work. GitHub + Node + Python.
		Name: "Engineering", Slug: "engineering",
		Color: "#3B82F6", Icon: "terminal",
		RuntimeImage: seedBaseImage,
		DevcontainerConfig: crewConfigJSON(
			`,"ghcr.io/devcontainers/features/github-cli:1":{}`,
		),
		MiseConfig: `{"tools":{"node":"22","python":"3.12"}}`,
	},
	{
		// Quality: testing + security audits. Python for scripting, jq/yq via common-utils.
		Name: "Quality", Slug: "quality",
		Color: "#10B981", Icon: "shield-check",
		RuntimeImage: seedBaseImage,
		DevcontainerConfig: crewConfigJSON(
			`,"ghcr.io/devcontainers/features/python:1":{}` +
				`,"ghcr.io/devcontainers/features/github-cli:1":{}`,
		),
	},
	{
		// DevOps: infra + cloud management. Full cloud CLI suite.
		Name: "DevOps", Slug: "devops",
		Color: "#EF4444", Icon: "server",
		RuntimeImage: seedBaseImage,
		DevcontainerConfig: crewConfigJSON(
			`,"ghcr.io/devcontainers/features/docker-in-docker:2":{}` +
				`,"ghcr.io/devcontainers/features/kubectl-helm-minikube:1":{}` +
				`,"ghcr.io/devcontainers/features/aws-cli:1":{}` +
				`,"ghcr.io/devcontainers/features/azure-cli:1":{}` +
				`,"ghcr.io/dhoeric/features/google-cloud-cli:1":{}` +
				`,"ghcr.io/devcontainers/features/terraform:1":{}` +
				`,"ghcr.io/devcontainers/features/github-cli:1":{}`,
		),
		MiseConfig: `{"tools":{"terraform":"1.9"}}`,
	},
	{
		// Research: data analysis + scraping. Python + Node for web work.
		Name: "Research", Slug: "research",
		Color: "#06B6D4", Icon: "telescope",
		RuntimeImage: seedBaseImage,
		DevcontainerConfig: crewConfigJSON(
			`,"ghcr.io/devcontainers/features/python:1":{}` +
				`,"ghcr.io/devcontainers/features/github-cli:1":{}`,
		),
		MiseConfig: `{"tools":{"python":"3.12"}}`,
	},
}
