package devcontainer

import (
	"fmt"
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
}

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
	// No-op (|| true) on images that don't ship it, including Alpine (no apt).
	sb.WriteString("RUN rm -f /etc/apt/sources.list.d/yarn.list 2>/dev/null || true\n")

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

	return sb.String(), nil
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
