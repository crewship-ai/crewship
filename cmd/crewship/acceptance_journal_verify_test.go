package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Acceptance for `crewship journal verify`, the command behind the Audit log
// page's "Journal chain verified · Verify now" banner
// (GET /api/v1/admin/journal/verify). The banner has three states (ok,
// broken, error) and the CLI must agree with each: the exit code is how cron
// and agents learn the chain is not intact.

func journalVerifyConfig(t *testing.T, status int, body string) (*settingsStub, string) {
	t.Helper()
	stub := &settingsStub{routes: map[string]func(map[string]any) (int, string){
		"GET /api/v1/admin/journal/verify": func(map[string]any) (int, string) { return status, body },
	}}
	return stub, profileStubConfig(t, stub.start(t).URL)
}

func TestAcceptance_JournalVerify_IntactChainExitsZero(t *testing.T) {
	stub, cfg := journalVerifyConfig(t, 200, `{"workspace_id":"ws_test","ok":true,"count":128,"checkpoints":2}`)
	out, err := runProfileCLI(t, cfg, "journal", "verify")
	if err != nil {
		t.Fatalf("an intact chain must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Journal chain OK") || !strings.Contains(out, "128") {
		t.Errorf("human output lacks the verdict and count:\n%s", out)
	}
	if len(stub.called("GET", "/api/v1/admin/journal/verify")) != 1 {
		t.Errorf("verify did not call the endpoint once: %+v", stub.calls)
	}
}

func TestAcceptance_JournalVerify_JSONIsTheWholeOfStdout(t *testing.T) {
	_, cfg := journalVerifyConfig(t, 200, `{"workspace_id":"ws_test","ok":true,"count":5,"checkpoints":0}`)
	out, err := runProfileCLI(t, cfg, "journal", "verify", "-f", "json")
	if err != nil {
		t.Fatalf("verify -f json: %v\n%s", err, out)
	}
	var res struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%q", err, out)
	}
	if !res.OK || res.Count != 5 {
		t.Errorf("decoded %+v", res)
	}
}

func TestAcceptance_JournalVerify_BrokenChainExitsNonZeroAndNamesTheBreak(t *testing.T) {
	_, cfg := journalVerifyConfig(t, 200, `{"workspace_id":"ws_test","ok":false,"count":40,"broken_seq":17,"broken_id":"jr_17","reason":"hash mismatch","break_count":1}`)
	out, err := runProfileCLI(t, cfg, "journal", "verify")
	if err == nil {
		t.Fatalf("a broken chain must exit non-zero; output: %s", out)
	}
	for _, want := range []string{"BROKEN", "17", "jr_17", "hash mismatch"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// Rows the chain cannot account for are not a clean journal even when the
// server says ok (the banner's warn state).
func TestAcceptance_JournalVerify_RepairableRowsAreNotClean(t *testing.T) {
	_, cfg := journalVerifyConfig(t, 200, `{"workspace_id":"ws_test","ok":true,"count":9,"repairable_count":1,`+
		`"repairable":[{"seq":4,"id":"jr_4","stored_priority":"low","emit_priority":"permanent"}]}`)
	out, err := runProfileCLI(t, cfg, "journal", "verify")
	if err == nil {
		t.Fatalf("unresolved priorities must exit non-zero; output: %s", out)
	}
	if !strings.Contains(out, "UNRESOLVED") {
		t.Errorf("output does not say the journal is unresolved:\n%s", out)
	}
}

// Below ADMIN the server answers 403; that is an auth exit, never a verdict.
func TestAcceptance_JournalVerify_ForbiddenIsAuthError(t *testing.T) {
	_, cfg := journalVerifyConfig(t, 403, `{"error":"forbidden"}`)
	out, err := runProfileCLI(t, cfg, "journal", "verify")
	if err == nil {
		t.Fatalf("403 must fail; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != 4 {
		t.Errorf("exit code = %d, want 4 (auth)", got)
	}
	if strings.Contains(out, "Journal chain OK") {
		t.Errorf("printed a verdict after a refusal:\n%s", out)
	}
}
