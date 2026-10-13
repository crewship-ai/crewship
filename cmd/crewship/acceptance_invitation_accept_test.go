package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAcceptance_InvitationAcceptBinary(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/auth/invitation/accept" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Token != "synthetic-accept-proof" || r.Header.Get("Authorization") != "Bearer fake-token" {
			w.WriteHeader(400)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	config := authPairStubConfig(t, server.URL)
	cmd := exec.Command(buildCrewshipBinary(t), "workspace", "invite", "accept", "--token-stdin", "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	cmd.Stdin = strings.NewReader("synthetic-accept-proof\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("accept binary: %v %s", err, out)
	}
	if calls != 1 {
		t.Fatalf("actual accept calls=%d want1", calls)
	}
}

func TestAcceptance_InvitationLifecycleBinary(t *testing.T) {
	hits := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"review-ws","slug":"review-ws"}]`))
			return
		}
		key := r.Method + " " + r.URL.Path
		hits[key]++
		switch key {
		case "POST /api/v1/workspaces/review-ws/invitations/inv-review/reissue":
			_, _ = w.Write([]byte(`{"id":"inv-review","email":"reader@example.test","role":"MEMBER","token":"wi1_0000000000000000000000000000000000000000000000000000000000000000"}`))
		case "DELETE /api/v1/workspaces/review-ws/invitations/inv-review":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := authPairStubConfig(t, server.URL)
	for _, action := range []string{"reissue", "revoke"} {
		out, err := runAuthPairCLI(t, config, "workspace", "invite", action, "inv-review", "--workspace", "review-ws", "--format", "json")
		if err != nil {
			t.Fatalf("%s binary: %v %s", action, err, out)
		}
	}
	if hits["POST /api/v1/workspaces/review-ws/invitations/inv-review/reissue"] != 1 || hits["DELETE /api/v1/workspaces/review-ws/invitations/inv-review"] != 1 {
		t.Fatal("missing actual lifecycle HTTP calls", hits)
	}
}
