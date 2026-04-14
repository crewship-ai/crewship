package devcontainer

import (
	"errors"
	"strings"
	"testing"
)

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
		"remoteUser": "vscode"
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
	if c.RemoteUser != "vscode" {
		t.Errorf("RemoteUser = %q, want vscode", c.RemoteUser)
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
	registry, repo, tag, err := ParseFeatureRef("ghcr.io/devcontainers/features/python:1")
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
}

func TestParseFeatureRefInvalid(t *testing.T) {
	cases := []string{
		"python",           // no registry, no tag
		"python:1",         // no registry
		"ghcr.io/python:",  // empty tag
		"/repo:1",          // empty registry
		"ghcr.io/:1",       // empty repo
	}
	for _, ref := range cases {
		_, _, _, err := ParseFeatureRef(ref)
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
	input := `{"image": "ubuntu:22.04", "remoteUser": "dev"}`
	c, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.RemoteUser != "dev" {
		t.Errorf("RemoteUser = %q, want dev", c.RemoteUser)
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
