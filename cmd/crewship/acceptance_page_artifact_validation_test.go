package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Malformed `page artifact` invocations exit 2 and never mutate: the documented
// usage class, not the generic exit 1.
func TestAcceptance_PageArtifactLocalValidationExitsTwo(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"validation-ws","slug":"validation-ws","name":"Validation"}]`))
			return
		}
		writes.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: validation-ws\ntoken: crewship_cli_validation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := file("good.json", `{"entrypoint":"index.html","files":{}}`)
	binary := buildCrewshipBinary(t)
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"create without a name (A-04)", []string{"create", "demo", "--file", good}, "--name"},
		{"publish without a base version (A-05)", []string{"publish", "demo", "--file", good}, "--base-version"},
		{"create without a file", []string{"create", "demo", "--name", "Demo"}, "--file"},
		{"missing file", []string{"create", "demo", "--name", "Demo", "--file", filepath.Join(dir, "absent.json")}, "absent.json"},
		{"manifest is not an object", []string{"create", "demo", "--name", "Demo", "--file", file("array.json", `[]`)}, "JSON object"},
		{"unknown manifest field", []string{"create", "demo", "--name", "Demo", "--file", file("unknown.json", `{"surprise":1}`)}, "unknown manifest field"},
		{"manifest is not JSON", []string{"create", "demo", "--name", "Demo", "--file", file("broken.json", `{`)}, "manifest"},
		{"negative version", []string{"get", "demo", "--version", "-1"}, "nonnegative"},
		{"restore without versions (A-23)", []string{"restore", "demo"}, "--base-version"},
		{"share without versions", []string{"share", "demo"}, "--base-version"},
		{"reply without a version", []string{"reply", "demo", "comment-1", "--text", "hi"}, "--version"},
		{"comment without fields (A-30)", []string{"comment", "demo", "--text", "hi"}, "required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(binary, append([]string{"page", "artifact"}, c.args...)...)
			cmd.Env = append(os.Environ(), "HOME="+dir, "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
			out, err := cmd.CombinedOutput()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != 2 || !strings.Contains(string(out), c.want) {
				t.Fatalf("exit %d, want 2 naming %q\n%s", code, c.want, out)
			}
		})
	}
	if n := writes.Load(); n != 0 {
		t.Fatalf("%d invalid invocations reached the server", n)
	}
}
