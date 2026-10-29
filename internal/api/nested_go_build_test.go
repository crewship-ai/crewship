package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Captured before TestMain isolates the application HOME. No Go subprocess or
// build work runs in TestMain: resolution remains inside each measured test.
var apiTestOriginalHome string

// Keep the real CLI's application HOME and credentials isolated. Only its Go
// compiler subprocess reuses the developer/runner's normal module/build caches;
// otherwise Go derives fresh caches from the empty application fixture HOME.
func nestedGoBuildEnv(t *testing.T) []string {
	t.Helper()
	resolve := exec.CommandContext(t.Context(), "go", "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE")
	resolve.Env = append(os.Environ(), "HOME="+apiTestOriginalHome)
	out, err := resolve.Output()
	if err != nil {
		t.Fatalf("resolve original Go build caches: %v", err)
	}
	var caches map[string]string
	if err := json.Unmarshal(out, &caches); err != nil {
		t.Fatalf("decode Go build caches: %v", err)
	}
	env := os.Environ()
	for _, key := range []string{"GOPATH", "GOMODCACHE", "GOCACHE"} {
		if caches[key] == "" {
			t.Fatalf("Go returned empty %s", key)
		}
		env = append(env, key+"="+caches[key])
	}
	return env
}

func TestNestedGoBuildEnvPreservesApplicationIsolationAndExplicitCaches(t *testing.T) {
	applicationHome := t.TempDir()
	t.Setenv("HOME", applicationHome)
	t.Setenv("CREWSHIP_CONFIG", applicationHome+"/private-cli.yaml")
	explicit := map[string]string{
		"GOPATH": t.TempDir(), "GOMODCACHE": t.TempDir(), "GOCACHE": t.TempDir(),
	}
	for key, value := range explicit {
		t.Setenv(key, value)
	}
	resolved := map[string]string{}
	for _, entry := range nestedGoBuildEnv(t) {
		key, value, _ := strings.Cut(entry, "=")
		resolved[key] = value
	}
	for key, want := range explicit {
		if resolved[key] != want || os.Getenv(key) != want {
			t.Errorf("%s did not preserve explicit cache", key)
		}
	}
	if resolved["HOME"] != applicationHome || os.Getenv("HOME") != applicationHome || resolved["CREWSHIP_CONFIG"] != applicationHome+"/private-cli.yaml" {
		t.Fatal("build environment changed application isolation")
	}
}
