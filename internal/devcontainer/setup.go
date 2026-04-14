package devcontainer

import (
	"context"
	"fmt"
)

// EnsureAgentUser creates the `agent` user (UID 1001) and /home/agent with
// correct permissions if they don't exist. Safe to run multiple times.
// Required before any code runs as UID 1001 in a custom base image (debian,
// ubuntu, etc.) that doesn't pre-provision this user.
//
// The exec function is typically a method on an Installer or provider that
// delegates to docker exec. Its signature matches devcontainer.ExecFunc.
func EnsureAgentUser(ctx context.Context, containerID string, exec ExecFunc) error {
	script := `set -e
if ! getent group agent >/dev/null 2>&1; then
    groupadd -g 1001 agent 2>/dev/null || addgroup --gid 1001 agent 2>/dev/null || true
fi
if ! id -u agent >/dev/null 2>&1; then
    useradd -u 1001 -g 1001 -m -s /bin/bash agent 2>/dev/null || \
        adduser --uid 1001 --gid 1001 --home /home/agent --shell /bin/bash --disabled-password --gecos "" agent 2>/dev/null || true
fi
mkdir -p /home/agent
chown -R 1001:1001 /home/agent
chmod 755 /home/agent
`
	output, exitCode, err := exec(ctx, containerID, []string{"sh", "-c", script}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("exec ensure agent user: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("ensure agent user exit %d: %s", exitCode, output)
	}
	return nil
}

// EnsureClaudeCode installs the Claude Code CLI (`claude`) globally in the
// container if it's not already present. Required by agents using the
// CLAUDE_CODE adapter. Installs Node.js (apt + NodeSource 22.x) if needed,
// then `npm install -g @anthropic-ai/claude-code`.
//
// Idempotent — early-exits if `claude` is already on PATH. Used during
// devcontainer provisioning so the cached image always has the CLI ready.
func EnsureClaudeCode(ctx context.Context, containerID string, exec ExecFunc) error {
	script := `set -e
if command -v claude >/dev/null 2>&1; then
    exit 0
fi
if ! command -v node >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq
        apt-get install -y -qq --no-install-recommends ca-certificates curl gnupg
        curl -fsSL https://deb.nodesource.com/setup_22.x | bash - >/dev/null 2>&1
        apt-get install -y -qq --no-install-recommends nodejs
    else
        echo "ERROR: no apt-get; cannot install Node.js for Claude Code" >&2
        exit 1
    fi
fi
npm install -g @anthropic-ai/claude-code@latest --silent >/dev/null 2>&1 || \
    npm install -g @anthropic-ai/claude-code@latest 2>&1 | tail -10
command -v claude >/dev/null 2>&1 || { echo "claude install failed" >&2; exit 1; }
`
	output, exitCode, err := exec(ctx, containerID, []string{"sh", "-c", script}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("exec ensure claude code: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("ensure claude code exit %d: %s", exitCode, output)
	}
	return nil
}
