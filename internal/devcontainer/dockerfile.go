package devcontainer

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Build-context and in-image paths shared by the Dockerfile generator and the
// BuildKit image builder. Feature directories are staged into the build context
// under featureContextDir/{id} and copied into featureInstallBase/{id} inside
// the image — mirroring the container-commit installer's layout so feature
// install.sh scripts see the same paths.
const (
	featureContextDir  = "features"
	featureInstallBase = "/tmp/devcontainer-features"
)

// Option keys are uppercased before installation; any that don't reduce to a
// legal shell identifier (e.g. contain hyphens) are skipped via the shared
// envKeyRe (mise.go) rather than emitted as a broken inline assignment that
// would abort the RUN.

// DockerfileBuild is the input to GenerateDockerfile.
type DockerfileBuild struct {
	// BaseImage is the FROM image.
	BaseImage string
	// Features are the resolved features in install order (the output of
	// SortFeatures — common-utils first). Each becomes one COPY+RUN layer so
	// BuildKit caches per feature: adding a feature only rebuilds its layer and
	// everything after it.
	Features []*ResolvedFeature
	// OptionsByRef holds the operator-provided options per feature ref. Missing
	// entries fall back to feature-metadata defaults.
	OptionsByRef map[string]map[string]any
	// RootEnv is the operator's root-level devcontainer.json `containerEnv`. It
	// is applied as image ENV (root-wins over feature-declared containerEnv) so
	// the runtime PATH/vars the agent exec inherits are authoritative. Optional.
	RootEnv map[string]string
}

// containerEnvKeyRe validates an environment variable name. Unlike feature
// OPTIONS (which we uppercase before turning into shell assignments), a
// containerEnv key is a real env var name used verbatim and is case-sensitive,
// so we accept the natural [A-Za-z_][A-Za-z0-9_]* form rather than the
// uppercase-only envKeyRe.
var containerEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// containerEnvBadValueRe matches any ASCII control character (notably \n / \r).
// A Dockerfile `ENV KEY=value` cannot span lines without a trailing `\`, so a
// value containing a newline would break out of the ENV line and turn its tail
// into a new build directive — i.e. arbitrary build-time execution. These values
// flow from feature metadata and the operator's devcontainer.json (neither fully
// trusted), so values matching this are SKIPPED entirely (mirroring the
// invalid-key skip) rather than emitted.
var containerEnvBadValueRe = regexp.MustCompile(`[\x00-\x1f]`)

// GenerateDockerfile renders a deterministic Dockerfile that installs the given
// devcontainer features on top of BaseImage via BuildKit. It is pure: no Docker
// daemon, no filesystem — so it is fully unit-testable. Determinism (sorted env
// keys, stable layer order) is required for BuildKit layer-cache hits.
func GenerateDockerfile(b DockerfileBuild) (string, error) {
	if strings.TrimSpace(b.BaseImage) == "" {
		return "", fmt.Errorf("devcontainer: base image is required")
	}

	var sb strings.Builder
	// The syntax directive enables RUN --mount=type=cache below.
	sb.WriteString("# syntax=docker/dockerfile:1\n")
	sb.WriteString("FROM " + b.BaseImage + "\n")

	// Remediate known-broken third-party apt repos that ship in some base
	// images BEFORE any feature runs `apt-get update`. The MS devcontainer
	// "language" images (go:1.x, universal:2, …) ship
	// /etc/apt/sources.list.d/yarn.list whose signing key has expired
	// upstream, so `apt-get update` fails with a GPG NO_PUBKEY error and the
	// very first feature install (common-utils) aborts with exit 100 —
	// breaking provisioning for an image the operator picked straight from our
	// catalog. Yarn isn't needed to provision a crew (npm/corepack cover it),
	// so we drop the offending source. This is the remediation hook for
	// known-broken repos; extend the rm list as new offenders surface.
	// Guarded on existence so it's a clean no-op on images that don't ship it
	// (including Alpine, no apt) — but an unexpected removal failure (e.g.
	// read-only fs, permissions) still aborts the build loudly rather than
	// leaving the broken repo in place to fail later with a vaguer error.
	sb.WriteString("RUN if [ -e /etc/apt/sources.list.d/yarn.list ]; then rm -f /etc/apt/sources.list.d/yarn.list; fi\n")

	for _, f := range b.Features {
		if f == nil {
			continue
		}
		id := f.Metadata.ID
		if !featureIDRe.MatchString(id) {
			return "", fmt.Errorf("devcontainer: invalid feature id %q", id)
		}
		ctxDir := featureContextDir + "/" + id
		destDir := featureInstallBase + "/" + id

		sb.WriteString("\n# feature: " + id + "\n")
		// Trailing slashes copy directory *contents* into destDir.
		sb.WriteString("COPY " + ctxDir + "/ " + destDir + "/\n")

		// Inline the install env so option values never leak into image layers
		// as ENV (which would persist and pollute the runtime environment).
		// Feature-install-contract vars (_REMOTE_USER, _REMOTE_USER_HOME, …)
		// shared with the exec-install path so install.sh scripts that rely on
		// $_REMOTE_USER_HOME (e.g. copying a tool out of it) work identically
		// whether the feature is baked via BuildKit or installed via exec.
		env := featureBuildEnv(f.Metadata, b.OptionsByRef[f.Ref])
		assigns := strings.Join(featureContractEnv(), " ")
		if len(env) > 0 {
			assigns += " " + strings.Join(env, " ")
		}

		// One RUN layer per feature. apt caches are mounted (harmless for
		// features that don't use apt); `bash -e` stops on first error so a
		// failed install surfaces as a build failure instead of a broken image.
		sb.WriteString("RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \\\n")
		sb.WriteString("    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \\\n")
		sb.WriteString("    cd " + destDir + " && \\\n")
		sb.WriteString("    " + assigns + " bash -e ./install.sh && \\\n")
		sb.WriteString("    rm -rf " + destDir + "\n")
	}

	// Apply containerEnv as image ENV. The devcontainer-feature spec says a
	// feature's containerEnv MUST be applied to the container environment, but
	// the agent exec inherits the IMAGE env, not /etc/environment — so a
	// feature that adds its bin dir to PATH (e.g. ansible's pipx dir
	// /usr/local/py-utils/bin) is invisible at runtime unless we bake it into
	// ENV here. Emitted AFTER the feature layers so the dirs exist and the env
	// lands in the final image. Root-level containerEnv wins over feature
	// values, mirroring aggregateFeatureRequirements (provisioner_install.go).
	for _, line := range containerEnvDirectives(b.Features, b.RootEnv) {
		sb.WriteString(line + "\n")
	}

	return sb.String(), nil
}

// containerEnvDirectives renders the aggregated containerEnv (feature-declared,
// first-feature-wins; root-level overrides) as deterministic Dockerfile ENV
// lines. PATH is special-cased: every PATH contribution is merged into ONE
// `ENV PATH=<dirs>:$PATH` so feature bin dirs accumulate instead of clobbering
// each other (a second `ENV PATH=` would drop the first feature's dir). Root
// PATH dirs are placed leftmost (highest search precedence). Non-PATH values
// are emitted as individual `ENV KEY=value` lines, sorted for stable output.
func containerEnvDirectives(features []*ResolvedFeature, rootEnv map[string]string) []string {
	nonPath := map[string]string{}  // key -> value (first feature wins, root overrides)
	var featPath, rootPath []string // accumulated PATH dirs, deduped
	seen := map[string]bool{}

	addPath := func(dst *[]string, val string) {
		val = strings.ReplaceAll(val, "${containerEnv:", "${")
		for _, tok := range strings.Split(val, ":") {
			tok = strings.TrimSpace(tok)
			if tok == "" || isPathSelfRef(tok) {
				continue
			}
			if containerEnvBadValueRe.MatchString(tok) {
				continue // control char would break out of the ENV line
			}
			if !seen[tok] {
				seen[tok] = true
				*dst = append(*dst, tok)
			}
		}
	}

	// Features in install order; within a feature, sorted keys for determinism.
	for _, f := range features {
		if f == nil {
			continue
		}
		keys := make([]string, 0, len(f.Metadata.ContainerEnv))
		for k := range f.Metadata.ContainerEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := f.Metadata.ContainerEnv[k]
			if strings.EqualFold(k, "PATH") {
				addPath(&featPath, v)
				continue
			}
			if !containerEnvKeyRe.MatchString(k) {
				continue
			}
			if _, exists := nonPath[k]; !exists {
				nonPath[k] = v
			}
		}
	}

	// Root-level overrides feature-declared values; root PATH dirs lead.
	rootKeys := make([]string, 0, len(rootEnv))
	for k := range rootEnv {
		rootKeys = append(rootKeys, k)
	}
	sort.Strings(rootKeys)
	for _, k := range rootKeys {
		v := rootEnv[k]
		if strings.EqualFold(k, "PATH") {
			addPath(&rootPath, v)
			continue
		}
		if !containerEnvKeyRe.MatchString(k) {
			continue
		}
		nonPath[k] = v
	}

	var out []string
	keys := make([]string, 0, len(nonPath))
	for k := range nonPath {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if containerEnvBadValueRe.MatchString(nonPath[k]) {
			continue // control char (e.g. newline) would inject a new Dockerfile directive
		}
		out = append(out, "ENV "+k+"="+dockerfileEnvValue(nonPath[k]))
	}
	// Merge PATH: root dirs first (highest precedence), then feature dirs, then
	// the inherited $PATH so nothing already on PATH is lost.
	pathDirs := append(append([]string{}, rootPath...), featPath...)
	if len(pathDirs) > 0 {
		out = append(out, "ENV PATH="+strings.Join(pathDirs, ":")+":$PATH")
	}
	return out
}

// isPathSelfRef reports whether a PATH token is just a reference to the existing
// PATH (which we re-emit as the trailing $PATH) rather than a real directory.
func isPathSelfRef(tok string) bool {
	switch tok {
	case "$PATH", "${PATH}", "$ENV{PATH}":
		return true
	}
	return false
}

// dockerfileEnvValue normalizes a containerEnv value for an `ENV KEY=value`
// directive: devcontainer `${containerEnv:X}` references become Dockerfile
// `${X}` so they expand, and values containing whitespace are double-quoted
// (escaping backslash and quote, leaving `$` intact so variable refs expand).
func dockerfileEnvValue(v string) string {
	v = strings.ReplaceAll(v, "${containerEnv:", "${")
	if strings.ContainsAny(v, " \t") {
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `"`, `\"`)
		return `"` + v + `"`
	}
	return v
}

// featureBuildEnv returns the install-time environment for a feature as sorted
// KEY='single-quoted-value' assignments, mirroring buildFeatureEnv's
// default-then-override precedence but deterministically ordered for stable
// Dockerfile output. _CONTAINER_ID is intentionally omitted — it is a runtime
// container id that does not exist at image-build time.
func featureBuildEnv(meta FeatureMetadata, userOptions map[string]any) []string {
	pairs := make(map[string]string)
	// Defaults from metadata for options the operator didn't set.
	for key, spec := range meta.Options {
		if _, set := userOptions[key]; set {
			continue
		}
		specMap, ok := spec.(map[string]any)
		if !ok {
			continue
		}
		defVal, ok := specMap["default"]
		if !ok {
			continue
		}
		pairs[strings.ToUpper(key)] = fmt.Sprintf("%v", defVal)
	}
	// Operator-provided options win.
	for key, val := range userOptions {
		pairs[strings.ToUpper(key)] = fmt.Sprintf("%v", val)
	}

	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !envKeyRe.MatchString(k) {
			continue // skip keys that aren't legal shell identifiers
		}
		out = append(out, k+"="+shellSingleQuote(pairs[k]))
	}
	return out
}

// shellSingleQuote wraps s in single quotes, safely escaping any embedded
// single quote so the value survives the shell verbatim.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
