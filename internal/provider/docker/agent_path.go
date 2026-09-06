package docker

import "strings"

// wellKnownDevcontainerBinDirs are PATH entries that devcontainer features add
// for LOGIN shells only (via /etc/profile.d/*), so a non-login `docker exec`
// — how the agent CLI runs — never sees them. Prepended as the capture-failure
// fallback so feature/pipx tools stay reachable even when we couldn't capture
// the real login PATH at provision time.
//
//   - /usr/local/py-utils/bin    — pipx venv shims (ansible, poetry, …)
//   - /usr/local/share/npm-global/bin — global npm CLIs
//   - /home/agent/.local/bin     — pip --user / agent-installed tools
//   - /opt/mise/data/shims       — every mise-installed tool, including the
//     adapter CLIs (claude, codex, gemini-cli, …); outside the home volume
//     on purpose (internal/devcontainer/mise.go). Missing from this list
//     until 2026-09-06: a wizard-built crew whose only `claude` came from
//     mise answered every chat with "No such file or directory".
var wellKnownDevcontainerBinDirs = []string{
	"/usr/local/py-utils/bin",
	"/usr/local/share/npm-global/bin",
	"/home/agent/.local/bin",
	"/opt/crewship/bin",
	"/opt/mise/data/shims",
}

// defaultAgentPath is the last-resort PATH used when neither a captured login
// PATH nor an image ENV PATH is available. Mirrors the standard Debian/Ubuntu
// devcontainer base PATH so core utilities (mkdir, touch, …) never disappear.
const defaultAgentPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// applyAgentLoginPath ensures the container's env carries a PATH that a
// non-login `docker exec` can use to reach devcontainer-feature tools.
//
// The result is the union of the well-known dirs, the containerEnv PATH the
// build aggregated, and the captured login PATH, in that order; see the body.
//
// The resolved value replaces any existing PATH entry in env (or is appended),
// so subsequent execs that don't set their own PATH inherit it from the
// container config. imageEnv may be nil (image inspect failed) — handled.
func applyAgentLoginPath(env []string, loginPath string, imageEnv map[string]string) []string {
	// Sanitize at the point of use: a NUL byte in PATH makes runc reject the
	// entire container (`invalid environment variable "PATH": contains nul
	// byte`). loginPath is captured from a container exec stream and, in an
	// older crew_runtime_config, may have been PERSISTED with an undemuxed
	// stdcopy frame header (\x01\x00\x00…) still embedded. Stripping control
	// bytes here means an already-stored corrupt value can't brick container
	// start — a re-provision isn't required to recover.
	// Three sources, all kept, in this order, deduplicated:
	//   1. wellKnownDevcontainerBinDirs — reachable no matter what;
	//   2. the containerEnv PATH already in env — the build's aggregated
	//      PATH (agent tool dirs, feature-declared dirs, the image's own,
	//      expanded by expandContainerEnv before this runs);
	//   3. the captured login PATH — /etc/profile.d contributions a feature
	//      made without declaring containerEnv (pipx, nvm), then the image's.
	// Nothing here is authoritative on its own: on 2026-09-06 a BuildKit-built
	// crew's captured login PATH was the bare image PATH (mise writes nothing
	// to profile.d) and, used verbatim, replaced the aggregated PATH the build
	// had just written — every chat answered "claude: No such file or
	// directory". With no PATH from any source, defaultAgentPath.
	var parts []string
	seen := map[string]bool{}
	add := func(val string) {
		for _, d := range strings.Split(sanitizeEnvValue(val), ":") {
			d = strings.TrimSpace(d)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			parts = append(parts, d)
		}
	}
	for _, d := range wellKnownDevcontainerBinDirs {
		add(d)
	}
	add(envValue(env, "PATH"))
	add(loginPath)
	if len(parts) == len(wellKnownDevcontainerBinDirs) {
		if p := imageEnv["PATH"]; strings.TrimSpace(p) != "" {
			add(p)
		} else {
			add(defaultAgentPath)
		}
	}
	return replaceOrAppendEnv(env, "PATH", strings.Join(parts, ":"))
}

// sanitizeEnvValue drops control/NUL bytes and the U+FFFD replacement rune from
// a value bound for the container environment. NUL is the fatal one — runc
// rejects the container outright — but no legitimate PATH carries control
// bytes, so removing them is always safe.
func sanitizeEnvValue(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '�' || r == 0x7f || r < 0x20 {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fallbackAgentPath builds the capture-failure PATH: the well-known
// devcontainer bin dirs followed by the base PATH (first non-empty of the
// current env PATH, the image PATH, or defaultAgentPath), deduped so a dir
// already on the base path isn't repeated.
func fallbackAgentPath(envPath, imagePath string) string {
	base := envPath
	if strings.TrimSpace(base) == "" {
		base = imagePath
	}
	if strings.TrimSpace(base) == "" {
		base = defaultAgentPath
	}

	present := map[string]bool{}
	for _, d := range strings.Split(base, ":") {
		if d != "" {
			present[d] = true
		}
	}
	var prefix []string
	for _, d := range wellKnownDevcontainerBinDirs {
		if !present[d] {
			prefix = append(prefix, d)
		}
	}
	if len(prefix) == 0 {
		return base
	}
	return strings.Join(prefix, ":") + ":" + base
}

// envValue returns the value of key in a "KEY=VALUE" slice, or "" if absent.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):]
		}
	}
	return ""
}

// replaceOrAppendEnv sets key=value in a "KEY=VALUE" slice, replacing the first
// existing entry for key (preserving position) or appending when absent.
func replaceOrAppendEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
