package devcontainer

import (
	"context"
	"fmt"
	"strings"
)

// agentHome is the home directory every agent-side step runs against
// (UID 1001, created by the common-utils feature).
const agentHome = "/home/agent"

// crewToolsDir is the crew's persistent tool volume, mounted at container
// create (internal/provider/docker buildMounts) and prepended to the agent's
// PATH by scripts/entrypoint.sh.
const crewToolsDir = "/opt/crew-tools"

// AgentBinDir is an image-resident, agent-owned directory for binaries the
// build installs on the agent's behalf (the Factory droid CLI today). Not
// under the home volume and not under the tools volume, both of which hide
// image content at runtime; on the agent's PATH through AgentToolPathDirs.
const AgentBinDir = "/opt/crewship/bin"

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
// The same treatment goes to /opt/crew-tools, for the same reason one layer
// out. It is the crew's persistent tool directory — a named volume, on the
// agent's PATH via scripts/entrypoint.sh, and where anything meant to survive a
// container recreation is installed. Nothing in the tree created it: `grep -rn
// crew-tools` finds the mount, the backup collector and the PATH entry, and no
// mkdir and no chown anywhere in the build. A Docker named volume initialises
// from the image content at its mount point, so with the path absent from the
// image the volume came up root-owned 0755 against a container running as 1001,
// and the only thing that tried to populate it was the entrypoint's
//
//	mkdir -p /opt/crew-tools/bin 2>/dev/null || true
//
// which fails as 1001 and is swallowed twice — stderr discarded, and a `|| true`
// that has to be there so a failure cannot kill PID 1. Net effect: a directory
// on the agent's PATH that does not exist, nothing installable into it, and no
// error anywhere. Found by the runtime-conformance harness on docker 28.0.4,
// which writes to it as the agent and reports what actually happened (#1672).
//
// Creating it here rather than inventing a second mechanism: this function
// already exists to fix exactly this class of problem, already runs as root at
// the right point in the build, and is already idempotent.
//
// Runs as root, right after feature installation and before mise / lifecycle
// hooks. Recursive, but both paths hold only dotfiles at this point in the
// build, so the cost is negligible. Idempotent.
// ensureAgentAccountScript gives the runtime UID 1001 a passwd and group
// entry when the image has none. Every lifecycle hook and every agent run
// executes as the raw numeric user "1001:1001"; the entry used to exist only
// when the common-utils feature ran with uid=1001. A crew that declared
// common-utils with its default `username: automatic` (which reuses the base
// image's UID 1000 user), or whose image never ran it, got a process with no
// account: `sudo` reports "you do not exist in the passwd database" and the
// OpenSSH client refuses to start ("No user exists for uid 1001").
//
// It only appends, never rewrites: an existing 1001 entry (whatever its
// name) is left alone, and the name falls back to crewship-agent if
// "agent" already belongs to a different UID. No sudoers entry is written —
// the runtime container runs with no-new-privileges and all capabilities
// dropped, so root-requiring setup belongs in a feature (which installs as
// root), not in a postCreateCommand that calls sudo.
const ensureAgentAccountScript = `uid_taken() { awk -F: -v id="$2" '$3 == id { f = 1 } END { exit !f }' "$1"; } && ` +
	`name_taken() { awk -F: -v n="$2" '$1 == n { f = 1 } END { exit !f }' "$1"; } && ` +
	`if ! uid_taken /etc/passwd 1001; then ` +
	`n=agent; if name_taken /etc/passwd agent; then n=crewship-agent; fi; ` +
	`if ! uid_taken /etc/group 1001; then g=$n; if name_taken /etc/group "$g"; then g=crewship-agent; fi; echo "$g:x:1001:" >> /etc/group; fi; ` +
	`sh=/bin/sh; [ -x /bin/bash ] && sh=/bin/bash; ` +
	`echo "$n:x:1001:1001:Crewship agent:/home/agent:$sh" >> /etc/passwd; fi`

func EnsureAgentHomeOwnership(ctx context.Context, containerID string, exec ExecFunc) error {
	var b strings.Builder
	b.WriteString(ensureAgentAccountScript)
	b.WriteString(" && ")
	for i, dir := range []string{agentHome, crewToolsDir, AgentBinDir} {
		if i > 0 {
			b.WriteString(" && ")
		}
		fmt.Fprintf(&b, "mkdir -p %s && chown -R 1001:1001 %s && chmod 755 %s", dir, dir, dir)
	}
	stdout, exitCode, err := exec(ctx, containerID, []string{"sh", "-c", b.String()}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("agent home and tools ownership: %v", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("agent home and tools ownership exited %d: %s", exitCode, stdout)
	}
	return nil
}
