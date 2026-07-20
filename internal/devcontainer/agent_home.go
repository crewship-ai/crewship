package devcontainer

import (
	"context"
	"fmt"
)

// agentHome is the home directory every agent-side step runs against
// (UID 1001, created by the common-utils feature).
const agentHome = "/home/agent"

// EnsureAgentHomeOwnership hands /home/agent to the agent user before any
// step that runs as UID 1001.
//
// Feature install scripts run as root and freely create paths under the home
// they just provisioned, and some base images ship /home/agent owned by root
// outright. Everything downstream then runs as 1001 against a directory it
// cannot write: mise's gpg verification dies with
//
//	gpg: Fatal: can't create directory '/home/agent/.gnupg': Permission denied
//
// and the same trap is waiting for npm, pip, git and any postCreateCommand
// that touches a dotfile. Fixing it per-tool is whack-a-mole; the invariant is
// simply that the agent owns its own home.
//
// Runs as root, right after feature installation and before mise / lifecycle
// hooks. Recursive, but the home only holds dotfiles at this point in the
// build, so the cost is negligible. Idempotent.
func EnsureAgentHomeOwnership(ctx context.Context, containerID string, exec ExecFunc) error {
	stdout, exitCode, err := exec(ctx, containerID, []string{
		"sh", "-c", "mkdir -p " + agentHome + " && chown -R 1001:1001 " + agentHome + " && chmod 755 " + agentHome,
	}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("agent home ownership: %v", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("agent home ownership exited %d: %s", exitCode, stdout)
	}
	return nil
}
