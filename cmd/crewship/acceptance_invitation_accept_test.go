package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	out, err := runAuthPairCLI(t, config, "workspace", "invite", "accept", "synthetic-accept-proof", "--format", "json")
	if err != nil {
		t.Fatalf("accept binary: %v %s", err, out)
	}
	if calls != 1 {
		t.Fatalf("actual accept calls=%d want1", calls)
	}
}
