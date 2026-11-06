package pipeline

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
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
			var target *credentialTarget
			if tc.target != "" {
				ctx, target = withCredentialTarget(ctx, tc.target)
			}
			got, err := NewVaultCredentialResolver(db)(ctx, RunScope{WorkspaceID: "ws_test"}, "API_KEY")
			if tc.release {
				if err != nil || got != "tok" {
					t.Fatalf("got (%q, %v), want released", got, err)
				}
				// The resolver must tell the http step the value is host-bound and
				// which hosts are approved: the redirect check reads exactly this.
				if wantBound := tc.hosts != "" && tc.hosts != "[]"; target != nil && target.bound != wantBound {
					t.Fatalf("target.bound = %v, want %v", target.bound, wantBound)
				}
				if tc.hosts == `["api.partner.com"]` && (target == nil || len(target.hosts) != 1 || target.hosts[0] != "api.partner.com") {
					t.Fatalf("target.hosts = %+v, want [api.partner.com]", target)
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
	step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL, CredentialRef: &CredentialRef{Type: "API_KEY"}}}
	_, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{EgressTargets: []string{"127.0.0.1"}},
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_free"})
	if err == nil || hit {
		t.Fatalf("host-bound credential step proceeded: err=%v hit=%v", err, hit)
	}
}

// A redirect must not carry a host-bound credential to an unapproved host,
// even when the routine's egress_targets would allow that host. The resolver
// is stubbed because stored hosts must be public hostnames, which a loopback
// test server cannot be.
func TestWiredHTTPStep_HostBoundCredentialRefusesRedirectToOtherHost(t *testing.T) {
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCrew(t, db, "crew_r", "restricted", `["127.0.0.1","127.0.0.2"]`)
	var mu sync.Mutex
	hit := false
	otherListener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("second loopback address unavailable: %v", err)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hit = true
	}))
	other.Listener.Close()
	other.Listener = otherListener
	other.Start()
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer first.Close()

	exec := wiredHTTPExecutor(t, db)
	exec.credentialByType = func(ctx context.Context, _ RunScope, _ string) (string, error) {
		target, ok := credentialTargetFrom(ctx)
		if !ok {
			t.Fatal("no credential target in context")
		}
		target.bound, target.hosts = true, []string{"127.0.0.1"}
		return "vault-token-123", nil
	}
	step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: first.URL, CredentialRef: &CredentialRef{Type: "API_KEY"}}}
	_, _, _, err = exec.runHTTPStep(context.Background(), step, RenderContext{EgressTargets: []string{"127.0.0.1", "127.0.0.2"}},
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_r"})

	mu.Lock()
	defer mu.Unlock()
	if err == nil || hit {
		t.Fatalf("redirect reached the unapproved host: err=%v hit=%v", err, hit)
	}
	var blocked *EgressBlockedError
	if !errors.As(err, &blocked) || blocked.Rule != EgressRuleCredentialHosts {
		t.Fatalf("refused by another layer, so this proves nothing: %v", err)
	}
}
