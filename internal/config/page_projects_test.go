package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPageProjectsPathIsolation(t *testing.T) {
	dir := t.TempDir()
	crews := filepath.Join(dir, "crews")
	if err := os.Mkdir(crews, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validatePageProjectPath(filepath.Join(dir, "projects"), crews); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"relative", crews, filepath.Join(crews, "projects"), dir} {
		if err := validatePageProjectPath(p, crews); err == nil {
			t.Fatalf("accepted overlapping/relative path %q", p)
		}
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(crews, link); err != nil {
		t.Fatal(err)
	}
	if err := validatePageProjectPath(filepath.Join(link, "new"), crews); err == nil {
		t.Fatal("accepted symlinked crew storage")
	}
	c := Default()
	c.Storage.PageProjectsPath = crews
	c.Storage.BasePath = crews
	if err := c.Validate(); err == nil {
		t.Fatal("config validation did not enforce source isolation")
	}
}

func TestPageBuildImageRequiresPinnedToolchainAndStorage(t *testing.T) {
	if err := validatePageBuildImage("", ""); err != nil {
		t.Fatal(err)
	}
	if err := validatePageBuildImage("node:latest", "/projects"); err == nil {
		t.Fatal("accepted mutable toolchain tag")
	}
	image := "sha256:" + strings.Repeat("a", 64)
	if err := validatePageBuildImage(image, ""); err == nil {
		t.Fatal("accepted build without source storage")
	}
	if err := validatePageBuildImage(image, "/projects"); err != nil {
		t.Fatal(err)
	}
}

func TestDevelopmentRuntimeEnvironmentIsExplicit(t *testing.T) {
	for _, value := range []string{"", "false", "1", "true"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CREWSHIP_PAGE_RUNTIME_DEVELOPMENT_SAME_ORIGIN", value)
			c := Default()
			applyEnvOverrides(c)
			if got := c.Storage.PageRuntimeDevelopmentSameOrigin; got != (value == "true") {
				t.Fatalf("enabled=%v", got)
			}
			if err := validatePageRuntime("https://studio.example.com", "https://studio.example.com", c.Storage.PageRuntimeDevelopmentSameOrigin); (err == nil) != (value == "true") {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}

func TestPageStudioOriginDoesNotChangeInternalTransport(t *testing.T) {
	c := Default()
	c.Auth.NextjsURL = "http://127.0.0.1:8083"
	if c.PageStudioOrigin() != c.Auth.NextjsURL {
		t.Fatal("legacy fallback changed")
	}
	t.Setenv("CREWSHIP_PAGE_STUDIO_ORIGIN", "https://studio.example.com")
	t.Setenv("CREWSHIP_PAGE_RUNTIME_ORIGIN", "https://studio.example.com")
	t.Setenv("CREWSHIP_PAGE_RUNTIME_DEVELOPMENT_SAME_ORIGIN", "true")
	applyEnvOverrides(c)
	if c.Auth.NextjsURL != "http://127.0.0.1:8083" {
		t.Fatal("Page origin changed internal transport")
	}
	if c.PageStudioOrigin() != "https://studio.example.com" {
		t.Fatal("public origin not wired")
	}
	if err := validatePageRuntime(c.Storage.PageRuntimeOrigin, c.PageStudioOrigin(), c.Storage.PageRuntimeDevelopmentSameOrigin); err != nil {
		t.Fatal(err)
	}
}
