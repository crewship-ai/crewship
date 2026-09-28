package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestChatShareManagementCLI(t *testing.T) {
	var created, listed, revoked bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/agents" {
			_, _ = w.Write([]byte(`[{"id":"cagentagentagentagent","slug":"atlas"}]`))
			return
		}
		path := "/api/v1/agents/cagentagentagentagent/chats/chat-one/shares"
		switch {
		case r.URL.Path == path && r.Method == "POST":
			var body struct {
				TTL int64 `json:"ttl_seconds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TTL != 3600 {
				t.Errorf("TTL=%d err=%v", body.TTL, err)
			}
			created = true
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"share":{"id":"share-one"},"token":"cshr_synthetic"}`))
		case r.URL.Path == path && r.Method == "GET":
			listed = true
			_, _ = w.Write([]byte(`{"shares":[{"id":"share-one"}]}`))
		case r.URL.Path == path+"/share-one" && r.Method == "DELETE":
			revoked = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_TOKEN", "")
	flagServer = srv.URL
	cliCfg = &cli.CLIConfig{Server: srv.URL, Token: "management-token", Workspace: "cabcdefghijklmnopqrs"}
	_ = chatShareCreateCmd.Flags().Set("ttl", "1h")
	t.Cleanup(func() { _ = chatShareCreateCmd.Flags().Set("ttl", "24h") })
	if err := chatShareCreateCmd.RunE(chatShareCreateCmd, []string{"atlas", "chat-one"}); err != nil {
		t.Fatal(err)
	}
	if err := chatShareListCmd.RunE(chatShareListCmd, []string{"atlas", "chat-one"}); err != nil {
		t.Fatal(err)
	}
	if err := chatShareRevokeCmd.RunE(chatShareRevokeCmd, []string{"atlas", "chat-one", "share-one"}); err != nil {
		t.Fatal(err)
	}
	if !created || !listed || !revoked {
		t.Fatalf("create=%v list=%v revoke=%v", created, listed, revoked)
	}
}

func TestChatShareReadCLIUsesOnlyExplicitToken(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "refuse-redirect"}[redirect], func(t *testing.T) {
			redirected := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true; w.WriteHeader(200) }))
			defer target.Close()
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.URL.Path != "/api/v1/shared-chats/share-one/messages" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer cshr_synthetic" {
					t.Errorf("wrong scoped request path/auth")
				}
				if redirect {
					http.Redirect(w, r, target.URL, http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"shared text"}]}`))
			}))
			defer srv.Close()
			saveCLIState(t)
			flagServer = srv.URL
			cliCfg = &cli.CLIConfig{Token: "must-not-send-admin-token", Workspace: "must-not-send-workspace", Server: "https://other.invalid"}
			chatShareReadCmd.SetIn(strings.NewReader("cshr_synthetic\n"))
			_ = chatShareReadCmd.Flags().Set("token-stdin", "true")
			t.Cleanup(func() { chatShareReadCmd.SetIn(nil); _ = chatShareReadCmd.Flags().Set("token-stdin", "false") })
			err := chatShareReadCmd.RunE(chatShareReadCmd, []string{"share-one"})
			if redirect {
				if err == nil {
					t.Fatal("redirect accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !called || redirected {
				t.Fatalf("called=%v redirected=%v", called, redirected)
			}
		})
	}
}

func TestChatShareReadServerRejectsRemoteCleartext(t *testing.T) {
	for _, tt := range []struct {
		server  string
		allowed bool
	}{
		{"http://localhost:8080", true},
		{"http://127.0.0.1:8080", true},
		{"http://[::1]:8080", true},
		{"https://crewship.example", true},
		{"http://crewship.example", false},
		{"http://192.0.2.10:8080", false},
		{"http://[2001:db8::1]:8080", false},
	} {
		t.Run(tt.server, func(t *testing.T) {
			err := validateChatShareReadServer(tt.server)
			if (err == nil) != tt.allowed {
				t.Fatalf("validateChatShareReadServer(%q) error = %v, allowed = %t", tt.server, err, tt.allowed)
			}
		})
	}
}
