package main

// #5045 acceptance: a signed routine webhook configured through the CLI
// delivers a signal to parked wait:event steps by correlation key instead of
// starting a run.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestAcceptance_RoutineWebhookSignalDelivery(t *testing.T) {
	cfgPath, serverURL, db := startReliabilityAcceptanceServerDB(t)

	// Half-configured delivery is refused before any request is made.
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "create", "--slug", "rel-routine",
		"--signal-event-type", "payment.settled"); err == nil {
		t.Fatalf("create with only --signal-event-type succeeded:\n%s", out)
	}

	createOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "create",
		"--slug", "rel-routine", "--base-url", serverURL,
		"--signal-event-type", "payment.settled", "--signal-correlation-path", "order.id")
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, createOut)
	}
	var created struct {
		ID                    string `json:"id"`
		PublicURL             string `json:"public_url"`
		SigningSecret         string `json:"signing_secret"`
		SignalEventType       string `json:"signal_event_type"`
		SignalCorrelationPath string `json:"signal_correlation_path"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode: %v\n%s", err, createOut)
	}
	if created.SignalEventType != "payment.settled" || created.SignalCorrelationPath != "order.id" {
		t.Fatalf("signal config not echoed: %s", createOut)
	}

	arm := func(runID, key, filter string) {
		t.Helper()
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, created_at, updated_at)
			VALUES (?, ?, 'rel-pipe', 'rel-routine', 'waiting', 'run', ?, ?, ?)`, runID, reliabilityAcceptanceWorkspaceID, ts, ts, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := pipeline.NewSQLSignalWaitStore(db).ArmMatching(t.Context(), reliabilityAcceptanceWorkspaceID, runID, "gate",
			"payment.settled", filter, key, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	waitStatus := func(runID string) string {
		t.Helper()
		var st string
		if err := db.QueryRow(`SELECT status FROM pipeline_signal_waits WHERE run_id = ?`, runID).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	runCount := func() (n int) {
		_ = db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&n)
		return n
	}
	arm("sig-run-a", "A-1", "status == paid")
	arm("sig-run-b", "B-2", "")
	runsBefore := runCount()

	fire := func(delivery, body string, sign bool) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, created.PublicURL, bytes.NewReader([]byte(body)))
		req.Header.Set("Idempotency-Key", delivery)
		secret := created.SigningSecret
		if !sign {
			secret = "wrong-secret"
		}
		req.Header.Set("X-Crewship-Signature", "sha256="+hmacSHA256Hex(secret, []byte(body)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Bad signature: nothing is delivered.
	if code, _ := fire("d0", `{"order":{"id":"B-2"}}`, false); code != http.StatusUnauthorized {
		t.Fatalf("unsigned status = %d, want 401", code)
	}
	if waitStatus("sig-run-b") != "pending" {
		t.Fatal("unverified delivery woke a wait")
	}

	// Correlates with run A but its filter rejects the payload: ignored, still pending.
	code, out := fire("d1", `{"order":{"id":"A-1"},"status":"pending"}`, true)
	if code != http.StatusOK || out["status"] != "IGNORED" {
		t.Fatalf("filtered delivery = %d %v, want 200 IGNORED", code, out)
	}
	if waitStatus("sig-run-a") != "pending" {
		t.Fatal("non-matching payload consumed the wait")
	}

	// Matching delivery wakes only run A.
	code, out = fire("d2", `{"order":{"id":"A-1"},"status":"paid"}`, true)
	if code != http.StatusOK || out["status"] != "DELIVERED" || out["delivered"] != float64(1) {
		t.Fatalf("matching delivery = %d %v", code, out)
	}
	if waitStatus("sig-run-a") == "pending" || waitStatus("sig-run-b") != "pending" {
		t.Fatalf("wrong waits woken: a=%s b=%s", waitStatus("sig-run-a"), waitStatus("sig-run-b"))
	}

	// Redelivery of the same delivery id is idempotent.
	code, out = fire("d2", `{"order":{"id":"A-1"},"status":"paid"}`, true)
	if code != http.StatusOK || out["duplicate"] != true || out["status"] != "DELIVERED" {
		t.Fatalf("redelivery = %d %v, want duplicate DELIVERED", code, out)
	}

	// A new Idempotency-Key does not make the same signed body new work.
	code, out = fire("d2-fresh-key", `{"order":{"id":"A-1"},"status":"paid"}`, true)
	if code != http.StatusOK || out["duplicate"] != true {
		t.Fatalf("replay under a new key = %d %v, want duplicate", code, out)
	}
	// The same key with a different body is a conflict, not a duplicate.
	if code, _ := fire("d2", `{"order":{"id":"B-2"},"status":"paid"}`, true); code != http.StatusConflict {
		t.Fatalf("reused key with another body = %d, want 409", code)
	}
	if waitStatus("sig-run-b") != "pending" {
		t.Fatal("conflicting delivery woke a wait")
	}

	// Unknown correlation: accepted but ignored, and no run is started.
	code, out = fire("d3", `{"order":{"id":"nope"}}`, true)
	if code != http.StatusOK || out["status"] != "IGNORED" || out["reason"] != "no_matching_wait" {
		t.Fatalf("unmatched delivery = %d %v", code, out)
	}
	if got := runCount(); got != runsBefore {
		t.Fatalf("delivery mode created runs: %d -> %d", runsBefore, got)
	}

	// Clearing the signal config returns the webhook to starting runs.
	if out, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "update", created.ID, "--clear-signal"); err != nil {
		t.Fatalf("clear-signal: %v\n%s", err, out)
	}
	if code, out := fire("d4", `{"x":1}`, true); code != http.StatusAccepted {
		t.Fatalf("after --clear-signal status = %d %v, want 202 run start", code, out)
	}
	// Re-enabling needs both flags.
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "update", created.ID,
		"--signal-event-type", "x"); err == nil || !strings.Contains(out, "together") {
		t.Fatalf("partial update accepted: %v\n%s", err, out)
	}
}
