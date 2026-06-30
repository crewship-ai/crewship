package devcontainer

import (
	"strings"
	"testing"
)

func TestGenerateDockerfile_BaseImageRequired(t *testing.T) {
	if _, err := GenerateDockerfile(DockerfileBuild{}); err == nil {
		t.Fatal("expected error for empty base image")
	}
}

func TestGenerateDockerfile_RemediatesBrokenYarnRepo(t *testing.T) {
	// The go:1.x / universal:2 base images ship a yarn.list with an expired
	// GPG key that breaks apt-get update during the first feature install.
	// The generated Dockerfile must drop it before any feature runs, BEFORE
	// the first feature COPY/RUN, so provisioning succeeds on those images.
	out, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "mcr.microsoft.com/devcontainers/go:1.23-bookworm",
		Features:  []*ResolvedFeature{{Metadata: FeatureMetadata{ID: "common-utils"}}},
	})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	rm := "rm -f /etc/apt/sources.list.d/yarn.list"
	if !strings.Contains(out, rm) {
		t.Errorf("Dockerfile missing yarn-repo remediation %q:\n%s", rm, out)
	}
	if i, j := strings.Index(out, rm), strings.Index(out, "# feature: common-utils"); i < 0 || j < 0 || i > j {
		t.Errorf("remediation must precede the first feature layer (rm@%d, feature@%d)", i, j)
	}
}

func TestGenerateDockerfile_InvalidFeatureID(t *testing.T) {
	_, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "ubuntu:22.04",
		Features:  []*ResolvedFeature{{Metadata: FeatureMetadata{ID: "bad id/../escape"}}},
	})
	if err == nil {
		t.Fatal("expected error for invalid feature id")
	}
}

func TestGenerateDockerfile_StructureAndOrder(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "mcr.microsoft.com/devcontainers/base:ubuntu",
		Features: []*ResolvedFeature{
			{Ref: "ghcr.io/devcontainers/features/common-utils:2", Metadata: FeatureMetadata{ID: "common-utils"}},
			{Ref: "ghcr.io/devcontainers/features/github-cli:1", Metadata: FeatureMetadata{ID: "github-cli"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// syntax directive must be the very first line so cache mounts are enabled.
	if !strings.HasPrefix(df, "# syntax=docker/dockerfile:1\n") {
		t.Fatalf("missing/misplaced syntax directive:\n%s", df)
	}
	if !strings.Contains(df, "FROM mcr.microsoft.com/devcontainers/base:ubuntu\n") {
		t.Fatalf("missing FROM:\n%s", df)
	}

	// One COPY + RUN per feature, in the given (install) order.
	wantSeq := []string{
		"COPY features/common-utils/ /tmp/devcontainer-features/common-utils/",
		"cd /tmp/devcontainer-features/common-utils &&",
		"COPY features/github-cli/ /tmp/devcontainer-features/github-cli/",
		"cd /tmp/devcontainer-features/github-cli &&",
	}
	last := 0
	for _, want := range wantSeq {
		idx := strings.Index(df[last:], want)
		if idx < 0 {
			t.Fatalf("missing or out-of-order fragment %q in:\n%s", want, df)
		}
		last += idx + len(want)
	}

	// Cache mount + the feature-install-contract identity must be present.
	if !strings.Contains(df, "--mount=type=cache,target=/var/cache/apt") {
		t.Fatalf("missing apt cache mount:\n%s", df)
	}
	if !strings.Contains(df, "_REMOTE_USER=agent") {
		t.Fatalf("missing _REMOTE_USER:\n%s", df)
	}
	// _REMOTE_USER_HOME must be set, or features whose install.sh copies out of
	// $_REMOTE_USER_HOME (e.g. a tool from ~/.local/bin) break the build.
	if !strings.Contains(df, "_REMOTE_USER_HOME=/home/agent") {
		t.Fatalf("missing _REMOTE_USER_HOME (BuildKit feature install would fail):\n%s", df)
	}
	if !strings.Contains(df, "bash -e ./install.sh") {
		t.Fatalf("missing install.sh invocation:\n%s", df)
	}
	// _CONTAINER_ID is runtime-only and must not appear at build time.
	if strings.Contains(df, "_CONTAINER_ID") {
		t.Fatalf("_CONTAINER_ID must not be emitted at build time:\n%s", df)
	}
}

func TestGenerateDockerfile_EnvDefaultsOverrideSortedQuoted(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "ubuntu:22.04",
		Features: []*ResolvedFeature{
			{
				Ref: "ghcr.io/devcontainers/features/node:1",
				Metadata: FeatureMetadata{
					ID: "node",
					Options: map[string]any{
						"version":    map[string]any{"default": "lts"},
						"nodeGypDep": map[string]any{"default": false},
						"bad-key":    map[string]any{"default": "skipme"}, // invalid env name → skipped
					},
				},
			},
		},
		OptionsByRef: map[string]map[string]any{
			"ghcr.io/devcontainers/features/node:1": {"version": "20"}, // overrides default "lts"
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// User override wins; default for the unset option is kept; keys uppercased
	// and sorted (NODEGYPDEP before VERSION); invalid key dropped.
	if !strings.Contains(df, "NODEGYPDEP='false' VERSION='20' bash -e ./install.sh") {
		t.Fatalf("env assignment wrong:\n%s", df)
	}
	if strings.Contains(df, "skipme") || strings.Contains(df, "BAD-KEY") {
		t.Fatalf("invalid env key should be skipped:\n%s", df)
	}
}

func TestGenerateDockerfile_ShellQuotesValues(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "ubuntu:22.04",
		Features: []*ResolvedFeature{
			{Ref: "r", Metadata: FeatureMetadata{ID: "tool", Options: map[string]any{
				"flags": map[string]any{"default": "a'b c"}, // embedded quote + space
			}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(df, `FLAGS='a'\''b c'`) {
		t.Fatalf("value not safely single-quoted:\n%s", df)
	}
}

// TestGenerateDockerfile_FeatureContainerEnvPATH is the regression guard for the
// ansible bug: the ansible feature installs into /usr/local/py-utils/bin (pipx)
// and declares a containerEnv adding that dir to PATH. If the generated image's
// ENV PATH doesn't include it, the agent (UID 1001) gets "command not found".
// The Dockerfile MUST emit an `ENV PATH=…/py-utils/bin:…:$PATH` line.
func TestGenerateDockerfile_FeatureContainerEnvPATH(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "ubuntu:22.04",
		Features: []*ResolvedFeature{
			{Ref: "ghcr.io/devcontainers-extra/features/ansible:2", Metadata: FeatureMetadata{
				ID:           "ansible",
				ContainerEnv: map[string]string{"PATH": "/usr/local/py-utils/bin:${PATH}"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	// An ENV PATH line that prepends the feature dir and keeps the existing PATH.
	if !strings.Contains(df, "ENV PATH=/usr/local/py-utils/bin:$PATH") {
		t.Fatalf("Dockerfile must emit ENV PATH including the feature dir:\n%s", df)
	}
	// The ENV must come AFTER the feature install layer, so it lands in the
	// final image (and the feature's files exist before PATH points at them).
	if envIdx, featIdx := strings.Index(df, "ENV PATH="), strings.Index(df, "# feature: ansible"); envIdx < 0 || featIdx < 0 || envIdx < featIdx {
		t.Errorf("ENV PATH must follow the feature layer (env@%d, feature@%d)", envIdx, featIdx)
	}
}

// TestGenerateDockerfile_ContainerEnvMergeAndOrder proves multiple PATH-adding
// features merge into a single ENV PATH (not one ENV per feature that would
// clobber each other), root-level containerEnv overrides feature-declared
// non-PATH values, root PATH dirs take precedence (leftmost), and non-PATH env
// is emitted as ENV directives.
func TestGenerateDockerfile_ContainerEnvMergeAndOrder(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "ubuntu:22.04",
		Features: []*ResolvedFeature{
			{Ref: "a", Metadata: FeatureMetadata{ID: "aaa", ContainerEnv: map[string]string{
				"PATH":      "/opt/aaa/bin:${PATH}",
				"CARGO":     "from-aaa",
				"SHAREDKEY": "from-aaa",
			}}},
			{Ref: "b", Metadata: FeatureMetadata{ID: "bbb", ContainerEnv: map[string]string{
				"PATH":      "/opt/bbb/bin:${PATH}",
				"SHAREDKEY": "from-bbb", // first feature (aaa) wins
			}}},
		},
		RootEnv: map[string]string{
			"PATH":      "/opt/root/bin:${PATH}", // root PATH dir leftmost
			"SHAREDKEY": "from-root",             // root overrides feature
		},
	})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	// Exactly one ENV PATH line, merging all three dirs with root leftmost.
	if got := strings.Count(df, "ENV PATH="); got != 1 {
		t.Fatalf("expected exactly 1 ENV PATH line, got %d:\n%s", got, df)
	}
	if !strings.Contains(df, "ENV PATH=/opt/root/bin:/opt/aaa/bin:/opt/bbb/bin:$PATH") {
		t.Fatalf("merged PATH order wrong (want root,aaa,bbb):\n%s", df)
	}
	// First feature wins for non-PATH shared key, but root overrides.
	if !strings.Contains(df, `ENV SHAREDKEY=from-root`) {
		t.Errorf("root-level containerEnv must override feature value:\n%s", df)
	}
	if strings.Contains(df, "from-aaa") && strings.Contains(df, "SHAREDKEY=from-aaa") {
		t.Errorf("feature SHAREDKEY should have been overridden by root:\n%s", df)
	}
	if !strings.Contains(df, "ENV CARGO=from-aaa") {
		t.Errorf("non-overridden feature env must still be emitted:\n%s", df)
	}
}

func TestGenerateDockerfile_NoContainerEnvNoEnvLines(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage: "ubuntu:22.04",
		Features:  []*ResolvedFeature{{Metadata: FeatureMetadata{ID: "plain"}}},
	})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	if strings.Contains(df, "\nENV ") {
		t.Errorf("no containerEnv → no ENV directives expected:\n%s", df)
	}
}

func TestGenerateDockerfile_Deterministic(t *testing.T) {
	build := DockerfileBuild{
		BaseImage: "ubuntu:22.04",
		Features: []*ResolvedFeature{
			{Ref: "r", Metadata: FeatureMetadata{ID: "multi", Options: map[string]any{
				"alpha":   map[string]any{"default": "1"},
				"beta":    map[string]any{"default": "2"},
				"gamma":   map[string]any{"default": "3"},
				"delta":   map[string]any{"default": "4"},
				"epsilon": map[string]any{"default": "5"},
			}}},
		},
	}
	first, err := GenerateDockerfile(build)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := GenerateDockerfile(build)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("non-deterministic output on iteration %d:\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}
