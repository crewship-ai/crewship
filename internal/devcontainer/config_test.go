package devcontainer

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// EnsureAgentUser — auto-injection of common-utils for the agent user
// ---------------------------------------------------------------------------

func TestEnsureAgentUser_InjectsWhenFeaturesEmpty(t *testing.T) {
	c := &Config{Image: "alpine:3.20"}
	if !c.EnsureAgentUser() {
		t.Fatal("expected injection, got no-op")
	}
	feat, ok := c.Features[commonUtilsFeatureRef]
	if !ok {
		t.Fatalf("expected %s key in Features, got %+v", commonUtilsFeatureRef, c.Features)
	}
	if feat["username"] != "agent" || feat["uid"] != "1001" || feat["gid"] != "1001" {
		t.Errorf("injected options wrong: %+v", feat)
	}
}

func TestEnsureAgentUser_NilConfig(t *testing.T) {
	var c *Config
	if c.EnsureAgentUser() {
		t.Fatal("nil config should report no-op, not crash")
	}
}

func TestEnsureAgentUser_IdempotentOnExistingCommonUtilsV2(t *testing.T) {
	c := &Config{
		Image: "alpine:3.20",
		Features: map[string]map[string]any{
			commonUtilsFeatureRef: {"username": "myuser", "uid": "2000"},
		},
	}
	if c.EnsureAgentUser() {
		t.Fatal("explicit common-utils should be respected (no-op), got injection")
	}
	if c.Features[commonUtilsFeatureRef]["username"] != "myuser" {
		t.Errorf("operator's username was overwritten: %+v", c.Features[commonUtilsFeatureRef])
	}
}

func TestEnsureAgentUser_IdempotentOnDifferentVersionTag(t *testing.T) {
	// Operator pinned :1 for compat — we still treat it as "opted in"
	// and don't double-inject :2 alongside.
	c := &Config{
		Image: "alpine:3.20",
		Features: map[string]map[string]any{
			"ghcr.io/devcontainers/features/common-utils:1": {"username": "agent"},
		},
	}
	if c.EnsureAgentUser() {
		t.Fatal("pinned :1 should be respected, got injection")
	}
	if _, has := c.Features[commonUtilsFeatureRef]; has {
		t.Errorf("auto-injected :2 alongside operator's :1: %+v", c.Features)
	}
}

// TestEnsureAgentUser_RejectsSiblingPrefix pins the CodeRabbit fix:
// `common-utils-extra:1` shares the prefix string with common-utils
// but is an entirely different feature that does NOT create the
// agent user. A naive HasPrefix check would silently suppress
// injection and leave the operator with the original "user 'agent'
// does not exist" footgun. isCommonUtilsRef requires a `:` or `@`
// after the canonical name.
func TestEnsureAgentUser_RejectsSiblingPrefix(t *testing.T) {
	c := &Config{
		Image: "alpine:3.20",
		Features: map[string]map[string]any{
			"ghcr.io/devcontainers/features/common-utils-extra:1": {},
		},
	}
	if !c.EnsureAgentUser() {
		t.Fatal("sibling prefix common-utils-extra must NOT suppress injection")
	}
	if _, has := c.Features[commonUtilsFeatureRef]; !has {
		t.Errorf("expected canonical common-utils:2 injected alongside common-utils-extra, got %+v", c.Features)
	}
}

func TestEnsureAgentUser_PreservesUnrelatedFeatures(t *testing.T) {
	c := &Config{
		Image: "alpine:3.20",
		Features: map[string]map[string]any{
			"ghcr.io/devcontainers/features/git:1": {},
		},
	}
	if !c.EnsureAgentUser() {
		t.Fatal("expected injection alongside git feature")
	}
	if _, has := c.Features["ghcr.io/devcontainers/features/git:1"]; !has {
		t.Error("git feature was dropped during inject")
	}
	if _, has := c.Features[commonUtilsFeatureRef]; !has {
		t.Error("common-utils not injected")
	}
}

func TestParseMinimal(t *testing.T) {
	input := `{"image": "mcr.microsoft.com/devcontainers/base:ubuntu"}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if c.Image != "mcr.microsoft.com/devcontainers/base:ubuntu" {
		t.Errorf("Image = %q, want mcr.microsoft.com/devcontainers/base:ubuntu", c.Image)
	}
	if len(c.Features) != 0 {
		t.Errorf("Features = %v, want empty", c.Features)
	}
	if c.PostCreateCommand != nil {
		t.Errorf("PostCreateCommand = %v, want nil", c.PostCreateCommand)
	}
	if c.PostStartCommand != nil {
		t.Errorf("PostStartCommand = %v, want nil", c.PostStartCommand)
	}
}

func TestParsePostStartCommandStringForm(t *testing.T) {
	input := `{
		"image": "debian:bookworm",
		"postStartCommand": "service postgresql start"
	}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	got := c.NormalizedPostStartCommands()
	if len(got) != 1 || got[0] != "service postgresql start" {
		t.Errorf("NormalizedPostStartCommands = %v, want [service postgresql start]", got)
	}
}

func TestParsePostStartCommandMapForm(t *testing.T) {
	input := `{
		"image": "debian:bookworm",
		"postStartCommand": {
			"2-db": "service postgresql start",
			"1-net": "ip link set eth0 up",
			"3-cache": "redis-server --daemonize yes"
		}
	}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	got := c.NormalizedPostStartCommands()
	// Map form preserves sorted-by-key ordering.
	want := []string{
		"ip link set eth0 up",
		"service postgresql start",
		"redis-server --daemonize yes",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestPostStartCommandExcludedFromHash(t *testing.T) {
	baseInput := `{"image": "debian:bookworm"}`
	withPostStartInput := `{
		"image": "debian:bookworm",
		"postStartCommand": "echo hello"
	}`
	base, err := ParseBytes([]byte(baseInput))
	if err != nil {
		t.Fatal(err)
	}
	withPostStart, err := ParseBytes([]byte(withPostStartInput))
	if err != nil {
		t.Fatal(err)
	}
	if base.Hash() != withPostStart.Hash() {
		t.Errorf("Hash differs when only postStartCommand changes — got %s vs %s", base.Hash(), withPostStart.Hash())
	}
}

func TestParseFull(t *testing.T) {
	input := `{
		"image": "mcr.microsoft.com/devcontainers/base:ubuntu",
		"features": {
			"ghcr.io/devcontainers/features/python:1": {"version": "3.11"},
			"ghcr.io/devcontainers/features/node:1": {"version": "20"}
		},
		"postCreateCommand": "pip install -r requirements.txt",
		"containerEnv": {"GO_ENV": "development", "PORT": "8080"},
		"remoteUser": "agent"
	}`

	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	if c.Image != "mcr.microsoft.com/devcontainers/base:ubuntu" {
		t.Errorf("Image = %q", c.Image)
	}
	if len(c.Features) != 2 {
		t.Errorf("len(Features) = %d, want 2", len(c.Features))
	}
	if c.RemoteUser != "agent" {
		t.Errorf("RemoteUser = %q, want agent", c.RemoteUser)
	}
	if len(c.ContainerEnv) != 2 {
		t.Errorf("len(ContainerEnv) = %d, want 2", len(c.ContainerEnv))
	}
	if c.ContainerEnv["GO_ENV"] != "development" {
		t.Errorf("ContainerEnv[GO_ENV] = %q", c.ContainerEnv["GO_ENV"])
	}
}

func TestPostCreateCommandString(t *testing.T) {
	input := `{"image": "ubuntu:22.04", "postCreateCommand": "echo hello"}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	cmds := c.NormalizedPostCreateCommands()
	if len(cmds) != 1 || cmds[0] != "echo hello" {
		t.Errorf("NormalizedPostCreateCommands() = %v, want [echo hello]", cmds)
	}
}

func TestPostCreateCommandArray(t *testing.T) {
	input := `{"image": "ubuntu:22.04", "postCreateCommand": ["echo a", "echo b"]}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	cmds := c.NormalizedPostCreateCommands()
	if len(cmds) != 2 || cmds[0] != "echo a" || cmds[1] != "echo b" {
		t.Errorf("NormalizedPostCreateCommands() = %v", cmds)
	}
}

func TestPostCreateCommandMap(t *testing.T) {
	input := `{"image": "ubuntu:22.04", "postCreateCommand": {"install": "npm install", "build": "npm run build"}}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	cmds := c.NormalizedPostCreateCommands()
	// Keys are sorted: build < install.
	if len(cmds) != 2 || cmds[0] != "npm run build" || cmds[1] != "npm install" {
		t.Errorf("NormalizedPostCreateCommands() = %v, want [npm run build, npm install]", cmds)
	}
}

func TestHashStability(t *testing.T) {
	input := `{
		"image": "ubuntu:22.04",
		"features": {"ghcr.io/devcontainers/features/python:1": {"version": "3.11"}},
		"containerEnv": {"A": "1", "B": "2"}
	}`

	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	h1 := c.Hash()
	h2 := c.Hash()
	if h1 != h2 {
		t.Errorf("Hash not stable: %q != %q", h1, h2)
	}

	// Parse again to ensure stability across instances.
	c2, _ := ParseBytes([]byte(input))
	if c.Hash() != c2.Hash() {
		t.Errorf("Hash differs across parse calls: %q != %q", c.Hash(), c2.Hash())
	}
}

func TestHashSensitivity(t *testing.T) {
	a := `{"image": "ubuntu:22.04", "features": {"ghcr.io/devcontainers/features/python:1": {"version": "3.11"}}}`
	b := `{"image": "ubuntu:22.04", "features": {"ghcr.io/devcontainers/features/node:1": {"version": "20"}}}`

	ca, _ := ParseBytes([]byte(a))
	cb, _ := ParseBytes([]byte(b))

	if ca.Hash() == cb.Hash() {
		t.Error("different features should produce different hashes")
	}
}

func TestValidateEmptyImage(t *testing.T) {
	input := `{"image": ""}`
	_, err := ParseBytes([]byte(input))
	if !errors.Is(err, ErrEmptyImage) {
		t.Errorf("expected ErrEmptyImage, got %v", err)
	}
}

func TestValidateNoImage(t *testing.T) {
	input := `{}`
	_, err := ParseBytes([]byte(input))
	if !errors.Is(err, ErrEmptyImage) {
		t.Errorf("expected ErrEmptyImage, got %v", err)
	}
}

func TestValidateMalformedFeatureRef(t *testing.T) {
	input := `{"image": "ubuntu:22.04", "features": {"python": {}}}`
	_, err := ParseBytes([]byte(input))
	if !errors.Is(err, ErrInvalidFeatureRef) {
		t.Errorf("expected ErrInvalidFeatureRef, got %v", err)
	}
}

func TestValidateInvalidImage(t *testing.T) {
	input := `{"image": "ubuntu 22.04"}`
	_, err := ParseBytes([]byte(input))
	if !errors.Is(err, ErrInvalidImage) {
		t.Errorf("expected ErrInvalidImage, got %v", err)
	}
}

func TestParseFeatureRefValid(t *testing.T) {
	registry, repo, tag, digest, err := ParseFeatureRef("ghcr.io/devcontainers/features/python:1")
	if err != nil {
		t.Fatalf("ParseFeatureRef: %v", err)
	}
	if registry != "ghcr.io" {
		t.Errorf("registry = %q, want ghcr.io", registry)
	}
	if repo != "devcontainers/features/python" {
		t.Errorf("repo = %q, want devcontainers/features/python", repo)
	}
	if tag != "1" {
		t.Errorf("tag = %q, want 1", tag)
	}
	if digest != "" {
		t.Errorf("digest = %q, want empty for tag form", digest)
	}
}

func TestParseFeatureRefDigest(t *testing.T) {
	const sha = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ref := "ghcr.io/devcontainers/features/python@" + sha
	registry, repo, tag, digest, err := ParseFeatureRef(ref)
	if err != nil {
		t.Fatalf("ParseFeatureRef: %v", err)
	}
	if registry != "ghcr.io" || repo != "devcontainers/features/python" {
		t.Errorf("registry/repo = %q/%q", registry, repo)
	}
	if tag != "" {
		t.Errorf("tag = %q, want empty for digest form", tag)
	}
	if digest != sha {
		t.Errorf("digest = %q, want %q", digest, sha)
	}
}

func TestParseFeatureRefCaseInsensitiveRegistry(t *testing.T) {
	// OCI registry component is case-insensitive; we normalize to lowercase.
	registry, _, _, _, err := ParseFeatureRef("GHCR.IO/devcontainers/features/python:1")
	if err != nil {
		t.Fatalf("ParseFeatureRef: %v", err)
	}
	if registry != "ghcr.io" {
		t.Errorf("registry = %q, want normalized lowercase 'ghcr.io'", registry)
	}
}

func TestParseFeatureRefInvalid(t *testing.T) {
	cases := []string{
		"python",                    // no registry, no tag
		"python:1",                  // no registry
		"ghcr.io/python:",           // empty tag
		"/repo:1",                   // empty registry
		"ghcr.io/:1",                // empty repo
		"ghcr.io/python@sha256:xyz", // non-hex digest body
		"ghcr.io/python@sha256:abc", // too-short digest body
		"ghcr.io/python@md5:0123456789abcdef0123456789abcd", // wrong algorithm
	}
	for _, ref := range cases {
		_, _, _, _, err := ParseFeatureRef(ref)
		if !errors.Is(err, ErrInvalidFeatureRef) {
			t.Errorf("ParseFeatureRef(%q) = %v, want ErrInvalidFeatureRef", ref, err)
		}
	}
}

func TestParseUnknownFields(t *testing.T) {
	input := `{
		"image": "ubuntu:22.04",
		"forwardPorts": [3000, 8080],
		"customizations": {"vscode": {"extensions": ["ms-python.python"]}},
		"hostRequirements": {"cpus": 4}
	}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes should ignore unknown fields: %v", err)
	}
	if c.Image != "ubuntu:22.04" {
		t.Errorf("Image = %q", c.Image)
	}
}

func TestParseEmptyFeatures(t *testing.T) {
	input := `{"image": "ubuntu:22.04", "features": {}}`
	c, err := ParseBytes([]byte(input))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(c.Features) != 0 {
		t.Errorf("Features = %v, want empty", c.Features)
	}
}

func TestParseViaReader(t *testing.T) {
	input := `{"image": "ubuntu:22.04", "remoteUser": "agent"}`
	c, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.RemoteUser != "agent" {
		t.Errorf("RemoteUser = %q, want agent", c.RemoteUser)
	}
}

func TestParseRemoteUserRejected(t *testing.T) {
	input := `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`
	_, err := ParseBytes([]byte(input))
	if err == nil {
		t.Fatal("ParseBytes: expected error for unsupported remoteUser, got nil")
	}
	if !errors.Is(err, ErrUnsupportedField) {
		t.Errorf("ParseBytes: error = %v, want ErrUnsupportedField", err)
	}
}

func TestMarshalJSON(t *testing.T) {
	c := &Config{
		Image:    "ubuntu:22.04",
		Features: map[string]map[string]any{"ghcr.io/devcontainers/features/go:1": {"version": "1.26"}},
	}
	data, err := c.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// Round-trip: the output should be valid JSON we can parse back.
	c2, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("ParseBytes round-trip: %v", err)
	}
	if c2.Image != c.Image {
		t.Errorf("round-trip Image = %q, want %q", c2.Image, c.Image)
	}
}

func TestNormalizedPostCreateCommandsNil(t *testing.T) {
	c := &Config{Image: "ubuntu:22.04"}
	cmds := c.NormalizedPostCreateCommands()
	if cmds != nil {
		t.Errorf("NormalizedPostCreateCommands() = %v, want nil", cmds)
	}
}

func TestInvalidJSON(t *testing.T) {
	_, err := ParseBytes([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
