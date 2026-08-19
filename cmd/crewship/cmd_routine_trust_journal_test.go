package main

// `crewship routine trust grant` / `revoke` and the audit trail they create,
// driven through the BUILT binary.
//
// This branch made both decisions emit a journal entry
// (approval.trust_granted / approval.trust_revoked) — disarming a human gate
// is the bluntest act the approval system offers and it used to leave no
// trace. The emission landed; nothing in the CLI acknowledged it. The command
// that writes an audit record and does not tell you where to read it has only
// done half the job, and the half it skipped is the half an operator needs
// three weeks later when someone asks who turned the gate off.
//
// The entries are also reachable ONLY by entry type: the emitter sets no
// crew_id, agent_id, mission_id or trace_id, so none of `journal`'s narrowing
// flags except --type can select them, and the exact type strings are not
// something anybody recalls from memory. Printing them at the moment of the
// decision is what makes the trail findable at all.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func trustCLIConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath,
		[]byte("token: test-token\nworkspace: c00000000000000000trs\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func trustStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/trust") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"wtg_ab12cd34","step_id":"publish",
				"definition_hash":"9f8e7d6c5b4a39281706","granted_at":"2026-08-18T10:00:00Z","live":true}`))
		case strings.Contains(r.URL.Path, "/trust") && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"id":"wtg_ab12cd34","revoked_at":"2026-08-18T11:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected route"}`))
		}
	}))
}

// A grant writes approval.trust_granted. The success output has to hand over
// the command that reads it back.
func TestAcceptance_RoutineTrustGrant_PointsAtTheJournalEntry(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := trustStub(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, trustCLIConfig(t), srv.URL,
		"routine", "trust", "grant", "triage-inbound", "--step", "publish",
		"--reason", "approved 12x, identical every time", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	// The exact type string, because it is the only selector that works.
	if !strings.Contains(out, "approval.trust_granted") {
		t.Errorf("grant output never names the entry type it just wrote:\n%s", out)
	}
	// And a runnable command, not just the noun.
	if !strings.Contains(out, "crewship journal") {
		t.Errorf("grant output does not show how to read the audit trail back:\n%s", out)
	}
}

// Same contract on the withdrawal side. A revoke is the entry an investigation
// is most often looking for — "it used to auto-approve and now it does not".
func TestAcceptance_RoutineTrustRevoke_PointsAtTheJournalEntry(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := trustStub(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, trustCLIConfig(t), srv.URL,
		"routine", "trust", "revoke", "triage-inbound", "wtg_ab12cd34",
		"--reason", "policy review", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "approval.trust_revoked") {
		t.Errorf("revoke output never names the entry type it just wrote:\n%s", out)
	}
	if !strings.Contains(out, "crewship journal") {
		t.Errorf("revoke output does not show how to read the audit trail back:\n%s", out)
	}
}

// The pointer is human affordance, not payload. --format json must stay a
// clean decode of the server's response — an agent parsing it should not have
// to strip advice out of stdout.
func TestAcceptance_RoutineTrustGrant_JSONStaysClean(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := trustStub(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, trustCLIConfig(t), srv.URL,
		"routine", "trust", "grant", "triage-inbound", "--step", "publish", "--format", "json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "crewship journal") {
		t.Errorf("--format json leaked the human pointer into the payload:\n%s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("--format json did not produce a bare JSON object:\n%s", out)
	}
}
