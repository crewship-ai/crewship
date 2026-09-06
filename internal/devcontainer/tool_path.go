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
	AgentBinDir,
	MiseShimsDir,
}

// imagePathRef is what stands in for the image's own PATH when the config
// declares none: the devcontainer reference the Dockerfile generator turns
// into `$PATH`, the runtime expands against the image env at container
// create, and the commit path resolves against the base image before
// `docker commit`. A hard-coded Debian default here would replace a base
// image's own PATH (golang, python images add /go/bin, /usr/local/go/bin …).
const imagePathRef = "${containerEnv:PATH}"

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
		current = imagePathRef
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
// value with spaces or quotes cannot break the instruction. `${containerEnv:X}`
// and `${X}` references are expanded against imageEnv (the base image's env):
// a committed ENV line gets no build-time substitution, unlike a Dockerfile
// ENV. A reference that imageEnv cannot answer is dropped. Keys that are not
// legal environment names (envKeyRe, the same rule as the Dockerfile path)
// and values with control characters are skipped rather than committed
// broken.
func imageEnvChanges(env, imageEnv map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if !envKeyRe.MatchString(k) || strings.ContainsAny(env[k], "\n\r\x00") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, "ENV "+k+"="+strconv.Quote(expandEnvRefs(env[k], imageEnv)))
	}
	return out
}

// expandEnvRefs replaces `${containerEnv:X}` and `${X}` with lookup[X]; an
// unknown X becomes the empty string, and the PATH separators around it are
// collapsed so "a:${containerEnv:PATH}" with no image PATH is "a", not "a:".
func expandEnvRefs(v string, lookup map[string]string) string {
	v = strings.ReplaceAll(v, "${containerEnv:", "${")
	for {
		start := strings.Index(v, "${")
		if start < 0 {
			break
		}
		end := strings.Index(v[start:], "}")
		if end < 0 {
			break
		}
		name := v[start+2 : start+end]
		v = v[:start] + lookup[name] + v[start+end+1:]
	}
	for strings.Contains(v, "::") {
		v = strings.ReplaceAll(v, "::", ":")
	}
	return strings.Trim(v, ":")
}
