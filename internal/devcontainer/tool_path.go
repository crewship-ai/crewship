package devcontainer

import (
	"sort"
	"strconv"
	"strings"
)

// Where the agent user's own tools land, and why the image PATH must say so.
//
// mise installs every tool under /opt/mise/data/shims (mise.go: outside the
// home volume on purpose) and pip --user / agent-installed CLIs under
// /home/agent/.local/bin. A login shell
// finds them through /etc/profile.d; the agent runs through a non-login
// `docker exec` and does not. The BuildKit path cannot capture a login PATH
// (provisioner_build.go), and the runtime fallback only knew the feature
// dirs — so a wizard-built crew with `claude` installed by mise still
// answered "claude: No such file or directory". The seed and the read-time
// default config masked it by spelling these dirs out in containerEnv.PATH.
//
// AgentToolPathDirs is the one list; ensureAgentToolPath puts it at the front
// of the aggregated containerEnv PATH so the Dockerfile ENV (build path),
// /etc/environment (commit path) and the runtime container env all carry it.
var AgentToolPathDirs = []string{
	"/home/agent/.local/bin",
	MiseShimsDir,
}

// defaultImagePath is the PATH assumed when the config declares none — the
// standard Debian/Ubuntu devcontainer base PATH, mirroring the runtime's
// defaultAgentPath so the two never disagree about where `sh` lives.
const defaultImagePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// ensureAgentToolPath returns env with PATH starting with AgentToolPathDirs
// (deduplicated, existing order otherwise kept) and with the MISE_* variables
// the shims need to find their installs (MiseRuntimeEnv), unless the operator
// set them. A nil map is allocated. Values already right are left
// byte-identical, so re-provisioning does not churn.
func ensureAgentToolPath(env map[string]string) map[string]string {
	if env == nil {
		env = map[string]string{}
	}
	for _, kv := range MiseRuntimeEnv {
		if _, set := env[kv[0]]; !set {
			env[kv[0]] = kv[1]
		}
	}
	current := strings.TrimSpace(env["PATH"])
	if current == "" {
		current = defaultImagePath
	}
	present := map[string]bool{}
	for _, d := range strings.Split(current, ":") {
		if d != "" {
			present[d] = true
		}
	}
	var missing []string
	for _, d := range AgentToolPathDirs {
		if !present[d] {
			missing = append(missing, d)
		}
	}
	if len(missing) == 0 && env["PATH"] != "" {
		return env
	}
	env["PATH"] = strings.Join(append(missing, current), ":")
	return env
}

// imageEnvChanges renders env as `ENV KEY=value` Dockerfile instructions for
// `docker commit --change`, sorted for a stable image config. Values are
// double-quoted with Go escaping, which the Dockerfile parser accepts, so a
// value with spaces or quotes cannot break the instruction. Keys that are
// not valid environment names, and values with control characters, are
// skipped rather than committed broken.
func imageEnvChanges(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if !isEnvKey(k) || strings.ContainsAny(env[k], "\n\r\x00") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, "ENV "+k+"="+strconv.Quote(env[k]))
	}
	return out
}

func isEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
