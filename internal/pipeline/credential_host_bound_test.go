package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #5045: a host-bound credential is released to a routine only for a step
// whose real target host it names. The test goes red if the resolver stops
// consulting allowed_hosts.
func TestVaultCredentialResolver_HostBound(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	cases := []struct {
		name    string
		hosts   string // stored allowed_hosts; "" = column NULL
		target  string // "" = no target in context (secret templates)
		release bool
	}{
		{"unbound NULL, no target", "", "", true},
		{"unbound empty list, no target", "[]", "", true},
		{"unbound, any target", "", "evil.example.net", true},
		{"bound, approved target", `["api.partner.com"]`, "api.partner.com", true},
		{"bound, target case-insensitive", `["api.partner.com"]`, "API.Partner.com", true},
		{"bound, wildcard covers subdomain", `["*.partner.com"]`, "api.partner.com", true},
		{"bound, other target withheld", `["api.partner.com"]`, "evil.example.net", false},
		{"bound, no target (code/notify secrets) withheld", `["api.partner.com"]`, "", false},
		{"bound, unreadable list withheld", `not-json`, "api.partner.com", false},
		{"bound, invalid entry withheld", `["*"]`, "api.partner.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPolicyTestDB(t)
			defer db.Close()
			seedCredential(t, db, "c1", "ws_test", "", "API_KEY", "ACTIVE", "tok", "2026-01-01T00:00:00Z")
			if tc.hosts != "" {
				if _, err := db.Exec(`UPDATE credentials SET allowed_hosts = ? WHERE id = 'c1'`, tc.hosts); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if tc.target != "" {
				ctx, _ = withCredentialTarget(ctx, tc.target)
			}
			got, err := NewVaultCredentialResolver(db)(ctx, RunScope{WorkspaceID: "ws_test"}, "API_KEY")
			if tc.release {
				if err != nil || got != "tok" {
					t.Fatalf("got (%q, %v), want released", got, err)
				}
				return
			}
			if err == nil || got != "" {
				t.Fatalf("host-bound credential released: (%q, %v)", got, err)
			}
		})
	}
}

// The http step passes its real target host: a credential approved for a
// different host never leaves the server, and no request is sent.
func TestWiredHTTPStep_HostBoundCredentialNotSentToOtherHost(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCrew(t, db, "crew_free", "free", "")
	seedCredential(t, db, "c1", "ws_test", "", "API_KEY", "ACTIVE", "vault-token-123", "2026-01-01T00:00:00Z")
	if _, err := db.Exec(`UPDATE credentials SET allowed_hosts = '["api.partner.com"]' WHERE id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	exec := wiredHTTPExecutor(t, db)
	step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL, CredentialRef: &CredentialRef{Type: "api_key"}}}
	_, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{EgressTargets: []string{"127.0.0.1"}},
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_free"})
	if err == nil || hit {
		t.Fatalf("host-bound credential step proceeded: err=%v hit=%v", err, hit)
	}
}
