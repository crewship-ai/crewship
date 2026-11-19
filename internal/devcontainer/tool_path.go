package devcontainer

import (
	"sort"
	"strconv"
	"strings"
)

// AgentToolPathDirs is the shared availability list for provisioning, runtime
// creation and terminals. The first four entries are agent-owned locations;
// the final two are feature locations otherwise supplied only by login scripts.
// NativeToolPath only prepends missing entries: an operator's existing order
// remains authoritative.
//
// Declared mise pins precede the persistent home bin (#5459): the home volume
// survives image rebuilds, so an older self-installed or self-updated CLI there
// (a native Claude Code install writes ~/.local/bin/claude) would otherwise
// shadow the version the crew declares. Agent-installed tools that mise does
// not declare have no shim and still resolve from ~/.local/bin.
var AgentToolPathDirs = []string{
	AgentBinDir,
	MiseShimsDir,
	agentLocalBinDir,
	crewToolsDir + "/bin",
	"/usr/local/py-utils/bin",
	"/usr/local/share/npm-global/bin",
}

// DefaultAgentPath is used only when no source provides any PATH. Failed
// terminal inspection instead leaves PATH unset in exec env to inherit it.
const DefaultAgentPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// imagePathRef is what stands in for the image's own PATH when the config
// declares none: the devcontainer reference the Dockerfile generator turns
// into `$PATH`, the runtime expands against the image env at container
// create, and the commit path resolves against the base image before
// `docker commit`. A hard-coded Debian default here would replace a base
// image's own PATH (golang, python images add /go/bin, /usr/local/go/bin …).
const imagePathRef = "${containerEnv:PATH}"

// ensureAgentToolPath returns env with missing AgentToolPathDirs added to PATH
// (existing precedence preserved) and with the MISE_* variables
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
	env["PATH"] = CreationToolPath(current)
	return env
}

// agentLocalBinDir is the persistent home's user bin (pip --user, native CLI
// installers, agent-installed tools).
const agentLocalBinDir = agentHome + "/.local/bin"

// legacyGeneratedToolOrder is the native prefix Crewship itself wrote before
// #5459: into DefaultCrewDevcontainerConfig, the seeded crews and, through
// them, stored crew configs and runtime env. It is Crewship's own output, not
// an operator decision, so creation paths migrate this contiguous run (after
// de-duplication, wherever it occurs in a PATH) to the canonical order. An
// operator PATH that does not contain the run keeps its order.
var legacyGeneratedToolOrder = []string{agentLocalBinDir, AgentBinDir, MiseShimsDir}

// CreationToolPath is NativeToolPath for image provisioning and runtime
// creation. It additionally migrates legacyGeneratedToolOrder, so a crew whose
// stored config still carries the pre-#5459 literal gets declared pins first on
// its next build or container creation. Terminals deliberately use
// NativeToolPath on the created runtime's PATH instead: a container created
// before this change keeps one order for terminal and agent exec alike.
func CreationToolPath(sources ...string) string {
	migrated := make([]string, len(sources))
	for i, source := range sources {
		migrated[i] = migrateGeneratedToolOrder(source)
	}
	// Completing a PATH that lacks only the home bin prepends it ahead of the
	// pins and forms the generated run again; migrate once more so image ENV
	// and runtime creation agree (CreationToolPath is idempotent).
	return NativeToolPath(migrateGeneratedToolOrder(NativeToolPath(migrated...)))
}

// sanitizeToolPath drops control bytes and U+FFFD. Older captured login paths
// can contain undemuxed stdcopy bytes, and runc rejects NUL in env outright.
func sanitizeToolPath(source string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\uFFFD' {
			return -1
		}
		return r
	}, source)
}

func migrateGeneratedToolOrder(path string) string {
	path = sanitizeToolPath(path)
	// Match on first occurrences: a login capture where ~/.profile ran twice
	// (L:L:A:M) is the same generated order and must not keep L first.
	var parts []string
	seen := map[string]bool{}
	for _, part := range nativeToolPathParts(path) {
		key := strings.TrimSpace(part)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, part)
	}
	n := len(legacyGeneratedToolOrder)
	for i := 0; i+n <= len(parts); i++ {
		match := true
		for j, dir := range legacyGeneratedToolOrder {
			if strings.TrimSpace(parts[i+j]) != dir {
				match = false
				break
			}
		}
		if match {
			// Expand the run to the whole canonical list in place: otherwise
			// NativeToolPath would prepend the missing feature and crew-tools
			// directories ahead of the shims and let them shadow declared pins.
			// A directory the operator placed elsewhere keeps its position.
			elsewhere := map[string]bool{}
			for k, part := range parts {
				if k < i || k >= i+n {
					elsewhere[strings.TrimSpace(part)] = true
				}
			}
			out := append([]string(nil), parts[:i]...)
			for _, dir := range AgentToolPathDirs {
				if !elsewhere[dir] {
					out = append(out, dir)
				}
			}
			out = append(out, parts[i+n:]...)
			return strings.Join(out, ":")
		}
	}
	return path
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

// AgentToolProfile restores the image-owned tool directories after a POSIX
// login profile resets PATH. Non-login shells and child processes inherit
// the container ENV; this hook uses the same stable shims, never versioned
// install paths or run-scoped environment/credentials.
func AgentToolProfile() string {
	var b strings.Builder
	b.WriteString("# Generated by Crewship; native tool availability only.\n")
	b.WriteString("_crewship_tool_rest=${PATH-}\n_crewship_tool_keep=\n_crewship_tool_prefix=\n")
	b.WriteString(`while [ -n "$_crewship_tool_rest" ]; do` + "\n")
	b.WriteString("case $_crewship_tool_rest in *:*) _crewship_tool_dir=${_crewship_tool_rest%%:*}; _crewship_tool_rest=${_crewship_tool_rest#*:} ;; *) _crewship_tool_dir=$_crewship_tool_rest; _crewship_tool_rest= ;; esac\n")
	b.WriteString("case $_crewship_tool_dir in '') ;; *) case :$_crewship_tool_keep: in *:\"$_crewship_tool_dir\":*) ;; *) _crewship_tool_keep=${_crewship_tool_keep:+$_crewship_tool_keep:}$_crewship_tool_dir ;; esac ;; esac\ndone\n")
	b.WriteString("if [ -z \"$_crewship_tool_keep\" ]; then _crewship_tool_keep=" + shellQuote(DefaultAgentPath) + "; fi\n")
	for _, dir := range AgentToolPathDirs {
		b.WriteString("case :$_crewship_tool_keep: in *:" + shellQuote(dir) + ":*) ;; *) _crewship_tool_prefix=${_crewship_tool_prefix:+$_crewship_tool_prefix:}" + shellQuote(dir) + " ;; esac\n")
	}
	b.WriteString("PATH=${_crewship_tool_prefix:+$_crewship_tool_prefix:}$_crewship_tool_keep\nexport PATH\nunset _crewship_tool_rest _crewship_tool_keep _crewship_tool_prefix _crewship_tool_dir\n")
	return b.String()
}

// NativeToolPath merges PATH sources in order, preserving existing precedence,
// deduplicates them, and prepends only missing shared directories. A completed
// runtime PATH is a fixed point, so terminal exec and inherited agent exec
// select the same binary. Direct installation and feature paths are retained.
func NativeToolPath(sources ...string) string {
	seen := map[string]bool{}
	var parts []string
	for _, source := range sources {
		source = sanitizeToolPath(source)
		for _, dir := range nativeToolPathParts(source) {
			dir = strings.TrimSpace(dir)
			if dir != "" && !seen[dir] {
				seen[dir] = true
				parts = append(parts, dir)
			}
		}
	}
	if len(parts) == 0 {
		return NativeToolPath(DefaultAgentPath)
	}
	var prefix []string
	for _, dir := range AgentToolPathDirs {
		if !seen[dir] {
			prefix = append(prefix, dir)
		}
	}
	return strings.Join(append(prefix, parts...), ":")
}

// A provisioning PATH can still contain ${containerEnv:NAME}. Its colon is
// part of a reference, not a directory separator; expanding happens later.
func nativeToolPathParts(path string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(path); i++ {
		if strings.HasPrefix(path[i:], "${") {
			if end := strings.IndexByte(path[i+2:], '}'); end >= 0 {
				i += end + 2
				continue
			}
		}
		if path[i] == ':' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	return append(parts, path[start:])
}

// homeProfileSkeletonPattern matches the Debian/Ubuntu skeleton line in
// ~/.profile that prepends the persistent home bin unconditionally. A login
// shell runs ~/.profile last, so that line would put ~/.local/bin back ahead
// of the declared mise pins the login hook just restored (#5543).
const homeProfileSkeletonPattern = `^([[:space:]]*)PATH="\$HOME/\.local/bin:\$PATH"[[:space:]]*$`

// homeProfileGuardLine keeps the skeleton's intent (make ~/.local/bin
// available) but leaves an existing position alone.
const homeProfileGuardLine = `case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH="$HOME/.local/bin:$PATH" ;; esac # Crewship: keep declared mise pins first`

// HomeProfileSeeds are the image files a new home volume is seeded from: the
// image's own agent home and the skeleton scripts/entrypoint.sh copies on
// first boot. Existing home volumes are never rewritten.
var HomeProfileSeeds = []string{"/etc/skel/.profile", agentHome + "/.profile"}

// HomeProfileGuardScript rewrites exactly the skeleton line in each existing
// file and leaves every other byte, and every other profile shape, untouched.
// It is best effort: an image without sed keeps its profile as documented.
func HomeProfileGuardScript(files ...string) string {
	var b strings.Builder
	for _, file := range files {
		q := shellQuote(file)
		b.WriteString("if [ -f " + q + " ] && grep -qE " + shellQuote(homeProfileSkeletonPattern) + " " + q + " 2>/dev/null; then sed -i -E " +
			shellQuote("s|"+homeProfileSkeletonPattern+"|\\1"+homeProfileGuardLine+"|") + " " + q + " 2>/dev/null || true; fi\n")
	}
	return b.String()
}
