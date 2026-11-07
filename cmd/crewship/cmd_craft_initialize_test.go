package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCraftCLIInitializeWordFileKeepsAdmissionPrivateAndUsesIssuedLease(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("b", 64)
	native := []byte(`{"native":"word-model"}`)
	file := filepath.Join(t.TempDir(), "document.json")
	if err := os.WriteFile(file, native, 0600); err != nil {
		t.Fatal(err)
	}
	stub.OnPost(base+"/initialize", func(r *http.Request, raw []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != "" {
			t.Error("initializer sent an existing admission")
		}
		var body struct {
			Application string `json:"application_id"`
			Bytes       []byte `json:"bytes"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Application != "wordcraft" || !bytes.Equal(body.Bytes, native) {
			t.Error("initializer changed Word native bytes or app")
		}
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"new-public-session","application_id":"wordcraft","page_id":"page-1","generation":"9007199254740993","version":"1"}`), "application/json"
	})
	stub.OnPost(base+"/lease/release", func(r *http.Request, raw []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret || !strings.Contains(string(raw), `"generation":"9007199254740993"`) {
			t.Error("release lost issued private admission/string generation")
		}
		return 200, []byte(`{"holder":null}`), "application/json"
	})
	out, err := runPageCLI(t, `{"action":"release","generation":"9007199254740993"}`+"\n", "craft", "initialize", "health", "--file", file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "session_token") || !strings.Contains(out, `"version":"1"`) {
		t.Fatal("private token leaked or initial public receipt absent")
	}
	if len(stub.CallsFor("POST", base+"/initialize")) != 1 || len(stub.CallsFor("POST", base+"/sessions")) != 0 || len(stub.CallsFor("POST", base+"/lease")) != 0 {
		t.Fatal("initialize must use its single issued admission, without implicit acquire")
	}
}

// The real binary names the application on the wire, keeps Word as the default,
// refuses an unknown name before any request, and refuses an admission for a
// different application than the one it asked for.
func TestAcceptance_CraftInitializeApplication(t *testing.T) {
	var mu sync.Mutex
	var requested []string
	answer := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"grid-workspace","slug":"grid-workspace","name":"Grid"}]`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/api/v1/pages/grid/craft/initialize") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		var body struct {
			Application string `json:"application_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requested = append(requested, body.Application)
		issued := answer
		mu.Unlock()
		if issued == "" {
			issued = body.Application
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"session_token":"` + strings.Repeat("d", 64) + `","session_id":"public-session","application_id":"` + issued + `","page_id":"page-1","generation":"7","version":"1"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: grid-workspace\ntoken: crewship_cli_grid_initialize\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "native.json")
	if err := os.WriteFile(file, []byte(`{"native":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(extra ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"craft", "initialize", "grid", "--file", file}, extra...)...)
		cmd.Env = append(os.Environ(), "HOME="+dir, "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "wordcraft"},
		{[]string{"--application", "word"}, "wordcraft"},
		{[]string{"--application", "wordcraft"}, "wordcraft"},
		{[]string{"--application", "sheet"}, "gridcraft"},
		{[]string{"--application", "gridcraft"}, "gridcraft"},
	} {
		out, code := run(c.args...)
		if code != 0 || !strings.Contains(out, `"application_id":"`+c.want+`"`) {
			t.Fatalf("%v: exit %d\n%s", c.args, code, out)
		}
		if strings.Contains(out, strings.Repeat("d", 64)) {
			t.Fatalf("%v: private token leaked", c.args)
		}
		mu.Lock()
		last := requested[len(requested)-1]
		mu.Unlock()
		if last != c.want {
			t.Fatalf("%v: requested %q, want %q", c.args, last, c.want)
		}
	}
	mu.Lock()
	sent := len(requested)
	answer = "wordcraft"
	mu.Unlock()
	if out, code := run("--application", "slides"); code != 2 || !strings.Contains(out, "--application") {
		t.Fatalf("unknown application: exit %d\n%s", code, out)
	}
	mu.Lock()
	if len(requested) != sent {
		t.Error("an unknown application reached the server")
	}
	mu.Unlock()
	if out, code := run("--application", "sheet"); code == 0 || !strings.Contains(out, "invalid native initialization response") {
		t.Fatalf("a Word admission satisfied a Sheet request: exit %d\n%s", code, out)
	}
}
