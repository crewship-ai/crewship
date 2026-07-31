package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// `crewship journal verify` is the last line of defence in the tamper-evidence
// story: cron runs it, the test-harness runs it, and both act on the EXIT CODE.
// #1572 found it exiting 0 — printing "no tampering detected" — on a journal
// the server had just told it contained rows whose priority could not be
// accounted for. The CLI was not even decoding the field.
//
// These tests pin the two halves: the field is decoded and shown, and a
// non-empty `repairable` is a non-zero exit whatever `ok` says.

// mockVerifyServer serves one canned /api/v1/admin/journal/verify body.
func mockVerifyServer(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
}

// A server that reports ok=true while listing repairable rows is exactly the
// pre-#1572 server — and exactly the state the two-column downgrade produces.
// An up-to-date CLI must not relay that "OK": it must name the rows and exit
// non-zero, or every integrity cron in the fleet stays green through the
// attack.
func TestJournalVerify_RepairableRowsExitNonZeroEvenWhenServerSaysOK(t *testing.T) {
	saveCLIState(t)
	mockVerifyServer(t, `{
		"workspace_id":"cabcdefghijklmnopqrs",
		"ok":true,
		"count":42,
		"repairable":[{"seq":3,"id":"j_victim","stored_priority":"normal","emit_priority":"permanent"}],
		"repairable_count":1
	}`)

	out, err := captureStdoutCov(t, func() error {
		return journalVerifyCmd.RunE(journalVerifyCmd, nil)
	})
	if err == nil {
		t.Fatal("exit 0 on a journal with an unresolved priority — cron and the harness stay green " +
			"while a permanent entry sits downgraded and compaction-eligible")
	}
	if strings.Contains(out, "no tampering detected") {
		t.Errorf("reported a clean journal:\n%s", out)
	}
	// The operator has to be able to act on this, which means seeing WHICH row
	// and what the hash proves it was emitted with.
	for _, want := range []string{"j_victim", "permanent", "normal"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
}

// The same must hold when the server sends the count but trims the list (the
// cap that keeps an 86k-row workspace from answering with 86k items).
func TestJournalVerify_RepairableCountAloneStillExitsNonZero(t *testing.T) {
	saveCLIState(t)
	mockVerifyServer(t, `{"workspace_id":"cabcdefghijklmnopqrs","ok":true,"count":9,"repairable_count":7}`)

	out, err := captureStdoutCov(t, func() error {
		return journalVerifyCmd.RunE(journalVerifyCmd, nil)
	})
	if err == nil {
		t.Fatalf("exit 0 while the server reported 7 unresolved entries:\n%s", out)
	}
	if !strings.Contains(out, "7") {
		t.Errorf("the count never reached the operator:\n%s", out)
	}
}

// A genuinely clean journal must still read clean and exit 0 — a verifier that
// cries wolf is uninstalled, and then nothing is checked at all.
func TestJournalVerify_CleanChainStaysGreen(t *testing.T) {
	saveCLIState(t)
	mockVerifyServer(t, `{"workspace_id":"cabcdefghijklmnopqrs","ok":true,"count":42,"checkpoints":2}`)

	out, err := captureStdoutCov(t, func() error {
		return journalVerifyCmd.RunE(journalVerifyCmd, nil)
	})
	if err != nil {
		t.Fatalf("clean chain reported as a failure: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no tampering detected") {
		t.Errorf("clean chain did not read as clean:\n%s", out)
	}
}

// A broken chain keeps its existing behaviour: the break block, and non-zero.
func TestJournalVerify_BrokenChainStillExitsNonZero(t *testing.T) {
	saveCLIState(t)
	mockVerifyServer(t, `{
		"workspace_id":"cabcdefghijklmnopqrs",
		"ok":false,
		"count":42,
		"broken_seq":7,
		"broken_id":"j_broken",
		"reason":"content hash mismatch at seq 7",
		"break_count":1
	}`)

	out, err := captureStdoutCov(t, func() error {
		return journalVerifyCmd.RunE(journalVerifyCmd, nil)
	})
	if err == nil {
		t.Fatalf("exit 0 on a broken chain:\n%s", out)
	}
	if !strings.Contains(out, "BROKEN") || !strings.Contains(out, "j_broken") {
		t.Errorf("break output regressed:\n%s", out)
	}
}
