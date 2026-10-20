package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const artifactPreviewJSON = `{"chat":{"id":"chat-one","visibility":"private","audience":"author_and_workspace_operators"},"target":{"page_id":"page-one","type":"document","audience":"author_and_workspace_owners","expected":{"schema":3,"rev":7,"generation":2}},"can_update":false,"reason":"targeted_iteration_unavailable"}`

// Exercise argv, configuration, HTTP and output in the real CLI subprocess.
func runArtifactPreviewBinary(t *testing.T, server string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	argv := append([]string{"chat", "artifact-preview"}, args...)
	argv = append(argv, "--server", server, "--format", "json", "--no-color")
	cmd := exec.CommandContext(ctx, buildCrewshipBinary(t), argv...)
	cmd.Env = append(offlineEnv(t), "CREWSHIP_CONFIG="+os.Getenv("CREWSHIP_CONFIG"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestAcceptanceChatArtifactPreviewBinary(t *testing.T) {
	stub := pageStub(t)
	chatID, pageID := "chat%one", "page+one&review=original"
	path := "/api/v1/chats/" + chatID + "/artifact-context/preview"
	stub.OnGet(path, func(r *http.Request, body []byte) (int, []byte, string) {
		if got, want := r.URL.EscapedPath(), "/api/v1/chats/"+url.PathEscape(chatID)+"/artifact-context/preview"; got != want {
			t.Errorf("escaped route = %q, want %q", got, want)
		}
		wantQuery := url.Values{"page_id": {pageID}, "workspace_id": {covWSCli3}}
		if r.URL.RawQuery != wantQuery.Encode() {
			t.Errorf("query = %q, want %q", r.URL.RawQuery, wantQuery.Encode())
		}
		if r.Header.Get("Authorization") != "Bearer page-acceptance-token" {
			t.Error("preview did not use the configured human bearer token")
		}
		if len(body) != 0 {
			t.Errorf("read-only preview sent a body: %s", body)
		}
		return http.StatusOK, []byte(artifactPreviewJSON), "application/json"
	})
	stdout, stderr, err := runArtifactPreviewBinary(t, stub.URL(), chatID, "--page-id", pageID)
	if err != nil {
		t.Fatalf("artifact-preview: %v; stdout=%s stderr=%s", err, stdout, stderr)
	}
	var got, want any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("preview stdout is not JSON: %v: %s", err, stdout)
	}
	if err := json.Unmarshal([]byte(artifactPreviewJSON), &want); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("preview changed server eligibility or expected state: %s", stdout)
	}
	if calls := stub.Calls(); len(calls) != 1 {
		t.Fatalf("preview must make exactly one GET, got %+v", calls)
	}
}

func TestAcceptanceChatArtifactPreviewBinaryErrors(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			stub := pageStub(t)
			path := "/api/v1/chats/chat-one/artifact-context/preview"
			// Establish a successful prior read, then revoke or lose availability.
			stub.OnGet(path, clitest.JSONResponse(http.StatusOK, json.RawMessage(artifactPreviewJSON)))
			if stdout, stderr, err := runArtifactPreviewBinary(t, stub.URL(), "chat-one", "--page-id", "page-one"); err != nil {
				t.Fatalf("initial preview: %v: %s %s", err, stdout, stderr)
			}
			stub.OnGet(path, clitest.ErrorResponse(status, "preview unavailable"))
			stdout, stderr, err := runArtifactPreviewBinary(t, stub.URL(), "chat-one", "--page-id", "page-one")
			if err == nil {
				t.Fatalf("HTTP %d must fail: %s", status, stdout)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("HTTP %d printed a successful or stale preview: %s", status, stdout)
			}
			if !strings.Contains(stderr, "preview unavailable") {
				t.Fatalf("HTTP %d lost error explanation: %s", status, stderr)
			}
			for _, private := range []string{"author_and_workspace_operators", "author_and_workspace_owners", `"expected"`, `"generation"`} {
				if strings.Contains(stderr, private) {
					t.Fatalf("HTTP %d exposed prior preview %q: %s", status, private, stderr)
				}
			}
			if calls := stub.Calls(); len(calls) != 2 {
				t.Fatalf("error must not retry or fall back after the first read; got %+v", calls)
			}
		})
	}
}

func TestAcceptanceChatArtifactPreviewBinaryRequiresPageID(t *testing.T) {
	for _, args := range [][]string{{"chat-one"}, {"chat-one", "--page-id", ""}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			stub := pageStub(t)
			stdout, stderr, err := runArtifactPreviewBinary(t, stub.URL(), args...)
			if err == nil || !strings.Contains(stderr, "page-id") {
				t.Fatalf("missing --page-id must fail with a flag explanation: err=%v stdout=%s stderr=%s", err, stdout, stderr)
			}
			if len(stub.Calls()) != 0 {
				t.Fatalf("missing target reached HTTP: %+v", stub.Calls())
			}
		})
	}
}

func TestAcceptanceChatArtifactPreviewBinaryDoesNotFollowRedirect(t *testing.T) {
	pageStub(t) // isolated token/workspace/config for the real subprocess
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/api/v1/chats/chat-one/artifact-context/preview" {
			w.Header().Set("Location", "/redirected-preview")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(artifactPreviewJSON))
	}))
	defer server.Close()
	stdout, stderr, err := runArtifactPreviewBinary(t, server.URL, "chat-one", "--page-id", "page-one")
	if err == nil || strings.TrimSpace(stdout) != "" {
		t.Fatalf("redirect must not become an eligibility result: err=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("preview followed a redirect or retried: %d requests", got)
	}
}
