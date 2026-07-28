package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Route-level checks that only a real Router can make. The handler tests
// build a context by hand, which is the right way to isolate a gate but
// cannot prove the two things that actually break in production: that the
// routes are mounted at all, and that RequireAuth stamps the auth kind the
// gate reads.

// revealRouteRig builds a Router over a seeded DB with reveal fully enabled
// for an OWNER holding the capability, so any refusal below comes from the
// layer under test rather than the fixture.
func revealRouteRig(t *testing.T) (*Router, string) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	if _, err := db.Exec(`UPDATE workspaces SET credential_reveal_enabled = 1 WHERE id = ?`, wsID); err != nil {
		t.Fatalf("enable reveal: %v", err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET capabilities = ? WHERE workspace_id = ? AND user_id = ?`,
		SerializeCapabilities(map[string]struct{}{
			CapabilityChat: {}, CapabilityCredentialReveal: {},
		}), wsID, ownerID); err != nil {
		t.Fatalf("grant capability: %v", err)
	}
	InvalidateCapabilityCache(wsID, ownerID)
	seedCredentialEnc(t, db, wsID, ownerID, "cred-route", "GH_TOKEN", "ghp_routelevelvalue")

	tok := mintTokenFor(t, db, ownerID, "revealroute0000000000000000")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r, tok
}

// End-to-end L9 through the real middleware chain. Everything about this
// caller is legitimate — OWNER, capability granted, reveal enabled,
// substantive reason — and it is still refused, because a CLI token is the
// credential shape an agent or a leaked CI secret would present.
//
// The handler tests assert the same rule against a synthesized context; this
// one proves RequireAuth actually records the kind, which is the half that
// would silently regress if someone refactored the middleware.
func TestRevealRoute_CLITokenIsRefusedEndToEnd(t *testing.T) {
	r, tok := revealRouteRig(t)

	req := httptest.NewRequest("POST", "/api/v1/credentials/cred-route/reveal?workspace_id=test-workspace-id",
		strings.NewReader(`{"reason":"Migrating the deploy key into Vault for ticket OPS-812"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 — a CLI token must never reveal", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "ghp_routelevelvalue") {
		t.Fatal("refused reveal leaked the credential value")
	}
	// A 404 here would mean the route was never mounted and the mux fell
	// through — which would also produce a "denied-looking" test pass on a
	// handler that does not exist.
	if rr.Code == http.StatusNotFound {
		t.Fatal("route not mounted")
	}
}

// The reveal-policy literal path must win over /credentials/{credentialId}.
// Go 1.22's mux prefers the more specific pattern, but the two routes are
// registered in different places, so a future reshuffle could silently turn
// `GET /credentials/reveal-policy` into a lookup for a credential named
// "reveal-policy" — which answers 404 and looks like an unrelated bug.
func TestRevealRoute_PolicyPathBeatsTheCredentialWildcard(t *testing.T) {
	r, tok := revealRouteRig(t)

	req := httptest.NewRequest("GET", "/api/v1/credentials/reveal-policy?workspace_id=test-workspace-id", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "enabled") {
		t.Fatalf("body = %s, want the policy document — the credential wildcard probably swallowed the path", rr.Body.String())
	}
}

// Production wires a *journal.Writer, which is what makes the reveal handler
// usable at all — the router only wires the audit sink when the emitter can
// write synchronously. If *Writer ever stopped satisfying SyncEmitter, the
// type assertion in registerCrewsRoutes would quietly stop matching and every
// reveal in production would 500 with nothing to point at.
//
// internal/server/server.go:394 constructs exactly this type.
func TestRevealRoute_ProductionJournalWriterSatisfiesSyncEmitter(t *testing.T) {
	db := setupTestDB(t)
	w := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	t.Cleanup(func() { _ = w.Close() })

	var emitter journal.Emitter = w
	if _, ok := emitter.(journal.SyncEmitter); !ok {
		t.Fatal("*journal.Writer no longer satisfies journal.SyncEmitter — the reveal handler would silently lose its audit sink")
	}
}
