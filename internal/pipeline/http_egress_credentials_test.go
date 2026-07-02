package pipeline

// http_egress_credentials_test.go — pins the PRODUCTION enforcement of
// the http-step security perimeter that the DSL has always promised:
//
//   - egress_targets (routine layer) — declared allowlist enforced at
//     run time, empty list = unrestricted (the backward-compat
//     contract every pre-existing http routine relies on);
//   - crew network policy (crew/workspace layer) — the same
//     crews.network_mode / allowed_domains dial the sidecar proxy
//     enforces for agent_run egress, now applied to direct http steps;
//   - credential_ref — resolved from the workspace vault by TYPE and
//     injected into the outbound request, never anywhere else.
//
// The wired tests build their executor through NewWiredExecutor — the
// ONE production construction path — so they fail if the factory ever
// stops installing the gate/resolver (the exact "validated in the DSL,
// enforced by nobody" gap this file closes).

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// testEncryptionKey matches the convention in runner_llm_test.go.
// Not a real secret — a fixed hex pattern for the test cipher.
const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // gitleaks:allow

// openPolicyTestDB layers the crew network-policy columns (migration
// v18) + the credentials table (the two production sources the egress
// gate and credential resolver read) on top of the pipeline store
// schema, whose bare crews FK-target table lacks them. Column subset
// mirrors migrate_consts_v01_init.go + migrate_consts_v16_v25.go.
func openPolicyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if _, err := db.ExecContext(context.Background(), `
ALTER TABLE crews ADD COLUMN deleted_at TEXT;
ALTER TABLE crews ADD COLUMN network_mode TEXT NOT NULL DEFAULT 'free';
ALTER TABLE crews ADD COLUMN allowed_domains TEXT;
CREATE TABLE IF NOT EXISTS credentials (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	crew_id TEXT,
	name TEXT NOT NULL,
	encrypted_value TEXT NOT NULL,
	type TEXT NOT NULL DEFAULT 'SECRET',
	provider TEXT NOT NULL DEFAULT 'NONE',
	status TEXT NOT NULL DEFAULT 'ACTIVE',
	deleted_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		_ = db.Close()
		t.Fatalf("policy schema: %v", err)
	}
	return db
}

func seedCrew(t *testing.T, db *sql.DB, id, mode, domainsJSON string) {
	t.Helper()
	var domains any
	if domainsJSON != "" {
		domains = domainsJSON
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, network_mode, allowed_domains) VALUES (?, 'ws_test', ?, ?)`,
		id, mode, domains); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
}

func seedCredential(t *testing.T, db *sql.DB, id, wsID, crewID, credType, status, plainValue, createdAt string) {
	t.Helper()
	enc, err := encryption.Encrypt(plainValue)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	var crew any
	if crewID != "" {
		crew = crewID
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO credentials (id, workspace_id, crew_id, name, encrypted_value, type, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, crew, id, enc, credType, status, createdAt); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

// wiredHTTPExecutor builds an executor exactly the way production does
// (NewWiredExecutor with a DB) and opens the private-HTTP test hatch so
// httptest servers on 127.0.0.1 are reachable. The hatch bypasses ONLY
// the SSRF guards — the crew gate and routine egress_targets layers
// stay live, which is precisely what these tests exercise.
func wiredHTTPExecutor(t *testing.T, db *sql.DB) *Executor {
	t.Helper()
	exec := NewWiredExecutor(ExecutorDeps{
		Store:    NewStore(db),
		Resolver: NewResolver(db),
		Runner:   nil,
		DB:       db,
	})
	exec.SetAllowPrivateHTTPForTesting(true)
	return exec
}

// ---------------------------------------------------------------------------
// Crew network policy gate — unit semantics.
// ---------------------------------------------------------------------------

func TestCrewNetworkPolicyGate_Semantics(t *testing.T) {
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCrew(t, db, "crew_free", "free", "")
	seedCrew(t, db, "crew_restricted", "restricted", `["api.partner.com","127.0.0.1"]`)
	seedCrew(t, db, "crew_restricted_bare", "restricted", "")
	seedCrew(t, db, "crew_weird", "yolo", "")
	seedCrew(t, db, "crew_badjson", "restricted", `{not json`)

	gate := NewCrewNetworkPolicyGate(db)
	ctx := context.Background()

	cases := []struct {
		name  string
		crew  string
		host  string
		allow bool
	}{
		{"no crew in scope allows", "", "evil.example.com", true},
		{"missing crew row allows (v18 default free)", "crew_ghost", "evil.example.com", true},
		{"free mode allows anything", "crew_free", "evil.example.com", true},
		{"restricted allows listed host", "crew_restricted", "api.partner.com", true},
		{"restricted strips port before match", "crew_restricted", "127.0.0.1:8443", true},
		{"restricted is case-insensitive", "crew_restricted", "API.PARTNER.COM", true},
		{"restricted blocks unlisted host", "crew_restricted", "evil.example.com", false},
		{"restricted is exact-match (no subdomains, sidecar parity)", "crew_restricted", "sub.api.partner.com", false},
		{"restricted keeps sidecar default LLM domains", "crew_restricted_bare", "api.anthropic.com", true},
		{"restricted with no crew domains blocks the rest", "crew_restricted_bare", "api.partner.com", false},
		{"unknown mode fails closed", "crew_weird", "api.partner.com", false},
		{"malformed allowed_domains fails closed", "crew_badjson", "api.anthropic.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := gate(ctx, RunScope{WorkspaceID: "ws_test", AuthorCrewID: c.crew}, c.host)
			if c.allow && err != nil {
				t.Errorf("gate(%q, %q) = %v, want allow", c.crew, c.host, err)
			}
			if !c.allow && err == nil {
				t.Errorf("gate(%q, %q) allowed, want block", c.crew, c.host)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Vault credential resolver — unit semantics.
// ---------------------------------------------------------------------------

func TestVaultCredentialResolver_Semantics(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	db := openPolicyTestDB(t)
	defer db.Close()

	seedCredential(t, db, "cred_ws", "ws_test", "", "API_KEY", "ACTIVE", "ws-shared-token", "2026-01-01T00:00:00Z")
	seedCredential(t, db, "cred_crew", "ws_test", "crew_a", "API_KEY", "ACTIVE", "crew-a-token", "2026-01-02T00:00:00Z")
	seedCredential(t, db, "cred_other_crew", "ws_test", "crew_b", "GENERIC_SECRET", "ACTIVE", "crew-b-secret", "2026-01-03T00:00:00Z")
	seedCredential(t, db, "cred_pending", "ws_test", "", "SSH_KEY", "PENDING", "placeholder", "2026-01-04T00:00:00Z")
	seedCredential(t, db, "cred_foreign_ws", "ws_other", "", "CLI_TOKEN", "ACTIVE", "foreign-token", "2026-01-05T00:00:00Z")
	seedCredential(t, db, "cred_rotated_old", "ws_test", "", "SECRET", "ACTIVE", "old-secret", "2026-01-01T00:00:00Z")
	seedCredential(t, db, "cred_rotated_new", "ws_test", "", "SECRET", "ACTIVE", "new-secret", "2026-02-01T00:00:00Z")

	resolve := NewVaultCredentialResolver(db)
	ctx := context.Background()
	wsScope := RunScope{WorkspaceID: "ws_test"}
	crewAScope := RunScope{WorkspaceID: "ws_test", AuthorCrewID: "crew_a"}

	t.Run("resolves and decrypts by type", func(t *testing.T) {
		got, err := resolve(ctx, wsScope, "API_KEY")
		if err != nil || got != "ws-shared-token" {
			t.Errorf("got (%q, %v), want ws-shared-token", got, err)
		}
	})
	t.Run("type match is case-insensitive", func(t *testing.T) {
		got, err := resolve(ctx, wsScope, "api_key")
		if err != nil || got != "ws-shared-token" {
			t.Errorf("got (%q, %v), want ws-shared-token", got, err)
		}
	})
	t.Run("author-crew credential wins over workspace-shared", func(t *testing.T) {
		got, err := resolve(ctx, crewAScope, "API_KEY")
		if err != nil || got != "crew-a-token" {
			t.Errorf("got (%q, %v), want crew-a-token", got, err)
		}
	})
	t.Run("another crew's pinned credential is invisible", func(t *testing.T) {
		if got, err := resolve(ctx, crewAScope, "GENERIC_SECRET"); err == nil {
			t.Errorf("crew_b's credential leaked to crew_a scope: %q", got)
		}
	})
	t.Run("PENDING rows never resolve", func(t *testing.T) {
		if got, err := resolve(ctx, wsScope, "SSH_KEY"); err == nil {
			t.Errorf("PENDING placeholder resolved: %q", got)
		}
	})
	t.Run("workspace isolation", func(t *testing.T) {
		if got, err := resolve(ctx, wsScope, "CLI_TOKEN"); err == nil {
			t.Errorf("foreign workspace credential leaked: %q", got)
		}
	})
	t.Run("newest active row wins (rotation)", func(t *testing.T) {
		got, err := resolve(ctx, wsScope, "SECRET")
		if err != nil || got != "new-secret" {
			t.Errorf("got (%q, %v), want new-secret (rotated key must win)", got, err)
		}
	})
	t.Run("no match is an error, not empty success", func(t *testing.T) {
		if _, err := resolve(ctx, wsScope, "CERTIFICATE"); err == nil {
			t.Error("expected error for unmatched type")
		}
	})
	t.Run("missing workspace scope is an error", func(t *testing.T) {
		if _, err := resolve(ctx, RunScope{}, "API_KEY"); err == nil {
			t.Error("expected error for empty workspace scope")
		}
	})
}

// ---------------------------------------------------------------------------
// Factory-wired, end-to-end http step behaviour.
// ---------------------------------------------------------------------------

// TestWiredHTTPStep_CrewPolicy_BlocksAndAllows drives runHTTPStep on a
// factory-built executor: a restricted authoring crew must block an
// http step to an unlisted host BEFORE any bytes leave the process,
// and allow a listed one.
func TestWiredHTTPStep_CrewPolicy_BlocksAndAllows(t *testing.T) {
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCrew(t, db, "crew_locked", "restricted", `["api.partner.com"]`)
	seedCrew(t, db, "crew_open", "restricted", `["127.0.0.1"]`)
	exec := wiredHTTPExecutor(t, db)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL}}

	// Blocked: crew_locked does not list 127.0.0.1.
	_, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{},
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_locked"})
	var blocked *EgressBlockedError
	if err == nil || !errors.As(err, &blocked) {
		t.Fatalf("restricted crew: expected EgressBlockedError, got %v", err)
	}
	if blocked.Rule != EgressRuleCrewNetworkPolicy || blocked.StepID != "call" {
		t.Errorf("blocked error fields: %+v", blocked)
	}
	if hits != 0 {
		t.Errorf("blocked request still reached the server (%d hits)", hits)
	}

	// Allowed: crew_open lists the httptest host.
	out, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{},
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_open"})
	if err != nil || out != "ok" {
		t.Fatalf("allowed crew: got (%q, %v), want ok", out, err)
	}
	if hits != 1 {
		t.Errorf("allowed request should hit the server exactly once, got %d", hits)
	}
}

// TestWiredHTTPStep_RoutineEgressTargets_Contract pins the routine
// layer end to end on a factory-built executor, including the
// CRITICAL backward-compat contract: a routine that declares NO
// egress_targets keeps working against any (public) host — matching
// hostInEgressTargets and the DSL validator, which both treat the
// field as optional.
func TestWiredHTTPStep_RoutineEgressTargets_Contract(t *testing.T) {
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCrew(t, db, "crew_free", "free", "")
	exec := wiredHTTPExecutor(t, db)
	in := RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_free"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL}}

	t.Run("undeclared egress_targets stays unrestricted (back-compat)", func(t *testing.T) {
		out, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{}, in)
		if err != nil || out != "ok" {
			t.Fatalf("got (%q, %v), want ok — empty egress_targets must not restrict", out, err)
		}
	})
	t.Run("declared list covering the host passes", func(t *testing.T) {
		out, _, _, err := exec.runHTTPStep(context.Background(), step,
			RenderContext{EgressTargets: []string{"127.0.0.1"}}, in)
		if err != nil || out != "ok" {
			t.Fatalf("got (%q, %v), want ok", out, err)
		}
	})
	t.Run("declared list not covering the host blocks with structured error", func(t *testing.T) {
		_, _, _, err := exec.runHTTPStep(context.Background(), step,
			RenderContext{EgressTargets: []string{"api.partner.com"}}, in)
		var blocked *EgressBlockedError
		if err == nil || !errors.As(err, &blocked) {
			t.Fatalf("expected EgressBlockedError, got %v", err)
		}
		if blocked.Rule != EgressRuleRoutineTargets || blocked.Host != "127.0.0.1" {
			t.Errorf("blocked error fields: %+v", blocked)
		}
		if !strings.Contains(err.Error(), "egress_targets") {
			t.Errorf("error should tell the operator which knob to turn: %v", err)
		}
	})
}

// TestWiredHTTPStep_CredentialInjection pins the vault → header path on
// a factory-built executor: declared credential_ref of a matching type
// injects the decrypted value; no declaration (or no matching vault
// row) leaves the request untouched and still succeeds.
func TestWiredHTTPStep_CredentialInjection(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCrew(t, db, "crew_free", "free", "")
	seedCredential(t, db, "cred_api", "ws_test", "", "API_KEY", "ACTIVE", "vault-token-123", "2026-01-01T00:00:00Z")
	exec := wiredHTTPExecutor(t, db)
	in := RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_free"}

	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Run("declared credential_ref injects the vault value", func(t *testing.T) {
		step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{
			Method: "GET", URL: srv.URL,
			CredentialRef: &CredentialRef{Type: "api_key"},
		}}
		if _, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{}, in); err != nil {
			t.Fatalf("step: %v", err)
		}
		if seenAuth != "Bearer vault-token-123" {
			t.Errorf("Authorization = %q, want the decrypted vault credential", seenAuth)
		}
	})
	t.Run("no credential_ref leaves the request bare", func(t *testing.T) {
		step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL}}
		if _, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{}, in); err != nil {
			t.Fatalf("step: %v", err)
		}
		if seenAuth != "" {
			t.Errorf("Authorization = %q, want empty (no credential declared)", seenAuth)
		}
	})
	t.Run("unmatched type skips injection but still sends", func(t *testing.T) {
		step := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{
			Method: "GET", URL: srv.URL,
			CredentialRef: &CredentialRef{Type: "CERTIFICATE"},
		}}
		if _, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{}, in); err != nil {
			t.Fatalf("step: %v", err)
		}
		if seenAuth != "" {
			t.Errorf("Authorization = %q, want empty (no vault match)", seenAuth)
		}
	})
}
