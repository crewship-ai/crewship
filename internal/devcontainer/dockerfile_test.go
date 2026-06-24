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
