package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/client"
)

// imageEnvMap returns the image's default ENV as a key→value map. Used
// by expandContainerEnv to resolve ${VAR} references that devcontainer
// features sometimes emit literally (e.g. the Rust feature returns
// "PATH=/usr/local/cargo/bin:${PATH}" expecting shell expansion at
// container start; without it Docker stores the literal "${PATH}" and
// /usr/bin disappears from the runtime PATH, breaking mkdir/touch/etc.).
//
// Returns an empty map on inspect failure — callers fall back to
// passing values through unchanged so a transient inspect error never
// makes a configuration regression worse.
func imageEnvMap(ctx context.Context, cli *client.Client, image string) (map[string]string, error) {
	inspect, err := cli.ImageInspect(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("image inspect: %w", err)
	}
	out := make(map[string]string, len(inspect.Config.Env))
	for _, e := range inspect.Config.Env {
		if i := strings.IndexByte(e, '='); i > 0 {
			out[e[:i]] = e[i+1:]
		}
	}
	return out, nil
}

// expandContainerEnv expands shell-style ${VAR} references in the
// values of containerEnv against the image's default ENV. Each entry
// is in "KEY=VALUE" form; only VALUE is expanded. Unknown vars are
// left as-is (matches shell behaviour) so a typo doesn't silently
// produce an empty string.
//
// Devcontainer.dev/spec defines ${containerEnv:NAME} as the canonical
// form, but in the wild many features emit ${NAME} directly (e.g. the
// Rust feature returns "PATH=/usr/local/cargo/bin:${PATH}") — we
// support both for compatibility.
func expandContainerEnv(env []string, imageEnv map[string]string) []string {
	if len(imageEnv) == 0 {
		return env
	}
	out := make([]string, len(env))
	for i, e := range env {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			out[i] = e
			continue
		}
		key, value := e[:eq], e[eq+1:]
		out[i] = key + "=" + expandVarRefs(value, imageEnv)
	}
	return out
}

// expandVarRefs replaces ${VAR} and ${containerEnv:VAR} tokens in s
// with the corresponding value from vars. Tokens whose name isn't in
// the map are left as-is. Curly-brace form only — bare $VAR is not
// expanded (avoids accidentally rewriting things like literal regexes
// or shell scripts that happen to land in env values).
func expandVarRefs(s string, vars map[string]string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				// Unterminated — emit rest verbatim.
				b.WriteString(s[i:])
				break
			}
			name := s[i+2 : i+2+end]
			// Strip "containerEnv:" prefix if present (devcontainer-spec form).
			name = strings.TrimPrefix(name, "containerEnv:")
			if v, ok := vars[name]; ok {
				b.WriteString(v)
			} else {
				// Unknown var — preserve original token so the operator
				// can spot and debug it.
				b.WriteString(s[i : i+2+end+1])
			}
			i += 2 + end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
