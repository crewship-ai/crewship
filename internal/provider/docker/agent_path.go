package docker

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// Keep source selection here; all directory ordering and completion belongs to
// the same devcontainer function used by image provisioning and terminal exec.
const defaultAgentPath = devcontainer.DefaultAgentPath

// Merge the aggregated container PATH with the captured login additions; never
// replace it with the capture alone. In the 2026-09-06 incident a BuildKit
// capture contained only the bare image PATH and hid the mise shims that
// provisioning had added, so agent CLI execs failed with "not found".
// NativeToolPath also sanitizes persisted captures: older undemuxed exec output
// could contain stdcopy control/NUL bytes, and runc rejects an environment with
// NUL outright. Preserve that recovery when completing existing runtime inputs.
func applyAgentLoginPath(env []string, loginPath string, imageEnv map[string]string) []string {
	current := envValue(env, "PATH")
	sources := []string{current, loginPath}
	if agentPathSourcesNeedImage(sources...) {
		fallback := imageEnv["PATH"]
		if strings.TrimSpace(sanitizeEnvValue(fallback)) == "" {
			fallback = defaultAgentPath
		}
		sources = append(sources, fallback)
	}
	return replaceOrAppendEnv(env, "PATH", devcontainer.NativeToolPath(sources...))
}

// Sources containing only shared tool directories do not provide a usable
// image/system PATH. Restore the image as the final source in that case, as
// before centralization. Use the same shared list, without imposing another
// directory order. A genuinely custom source keeps its explicit restriction.
func agentPathSourcesNeedImage(sources ...string) bool {
	known := make(map[string]bool, len(devcontainer.AgentToolPathDirs))
	for _, dir := range devcontainer.AgentToolPathDirs {
		known[dir] = true
	}
	for _, source := range sources {
		for _, dir := range strings.Split(sanitizeEnvValue(source), ":") {
			if dir = strings.TrimSpace(dir); dir != "" && !known[dir] {
				return false
			}
		}
	}
	return true
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

// fallbackAgentPath completes the current path, or the image path if absent,
// through the same availability contract as normal runtime creation.
func fallbackAgentPath(envPath, imagePath string) string {
	if strings.TrimSpace(envPath) == "" {
		envPath = imagePath
	}
	return devcontainer.NativeToolPath(envPath)
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
