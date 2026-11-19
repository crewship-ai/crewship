package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

// Acceptance for #1910: a waitpoint's listed token is a public id, not a
// credential; the public callback URL is minted through the CLI, shown once,
// stored only as a digest, and rotated by minting again.
func TestAcceptance_WaitpointCallbackURL_MintedOnceAndHashed(t *testing.T) {
	srvURL, cfgPath, tok, _, db := startWaitpointDeciderAcceptanceServer(t)

	post := func(t *testing.T, url string) int {
		t.Helper()
		resp, err := http.Post(url, "application/json", bytes.NewBufferString(`{"approved":true}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	status := func(t *testing.T) (st, by string) {
		t.Helper()
		var byp sql.NullString
		if err := db.QueryRow(`SELECT status, decided_by_user_id FROM pipeline_waitpoints WHERE token = ?`, tok).Scan(&st, &byp); err != nil {
			t.Fatal(err)
		}
		return st, byp.String
	}

	// 1. Listing and showing hand out no capability.
	out, err := runWaitpointDeciderCLI(t, cfgPath, "routine", "waitpoints", "list", "--format", "json")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, tok) || !strings.Contains(out, `"callback_available": true`) || strings.Contains(out, "waitpoint-tokens") {
		t.Fatalf("list must carry the id and callback_available, never a callback URL:\n%s", out)
	}
	out, err = runWaitpointDeciderCLI(t, cfgPath, "routine", "waitpoints", "show", tok)
	if err != nil || strings.Contains(out, "waitpoint-tokens") || !strings.Contains(out, "callback-url "+tok) {
		t.Fatalf("show must point at callback-url, not print a URL: %v\n%s", err, out)
	}

	// 2. The listed id does not open the public callback.
	if code := post(t, srvURL+"/api/v1/waitpoint-tokens/"+tok); code != http.StatusConflict {
		t.Fatalf("public callback with the waitpoint id: %d, want 409", code)
	}
	if st, _ := status(t); st != "pending" {
		t.Fatalf("waitpoint id moved the gate to %s", st)
	}

	// 3. Mint through the CLI: the URL is printed once and never stored.
	mint := func(t *testing.T) string {
		t.Helper()
		out, err := runWaitpointDeciderCLI(t, cfgPath, "routine", "waitpoints", "callback-url", tok)
		if err != nil {
			t.Fatalf("callback-url: %v\n%s", err, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, srvURL+"/api/v1/waitpoint-tokens/wpt_") {
				return strings.TrimSpace(line)
			}
		}
		t.Fatalf("callback-url printed no URL:\n%s", out)
		return ""
	}
	first := mint(t)
	secret := first[strings.LastIndex(first, "/")+1:]
	var hits int
	for _, q := range []string{
		`SELECT COUNT(*) FROM pipeline_waitpoints WHERE instr(token, ?) > 0 OR instr(COALESCE(callback_token_hash,''), ?) > 0 OR instr(COALESCE(prompt,''), ?) > 0`,
		`SELECT COUNT(*) FROM inbox_items WHERE instr(source_id, ?) > 0 OR instr(COALESCE(payload_json,''), ?) > 0 OR instr(COALESCE(body_md,''), ?) > 0`,
		`SELECT COUNT(*) FROM audit_logs WHERE instr(entity_id, ?) > 0 OR instr(COALESCE(metadata,''), ?) > 0 OR instr(action, ?) > 0`,
	} {
		var n int
		if err := db.QueryRow(q, secret, secret, secret).Scan(&n); err != nil {
			t.Fatal(err)
		}
		hits += n
	}
	if hits != 0 {
		t.Fatalf("the minted callback secret is stored in the clear (%d rows)", hits)
	}
	out, _ = runWaitpointDeciderCLI(t, cfgPath, "routine", "waitpoints", "list", "--format", "json")
	if strings.Contains(out, secret) {
		t.Fatalf("list returned the minted secret")
	}

	// 4. Minting again rotates: the first URL is dead, the second decides.
	second := mint(t)
	if second == first {
		t.Fatal("re-mint returned the same URL")
	}
	if code := post(t, first); code != http.StatusConflict {
		t.Fatalf("rotated-out URL: %d, want 409", code)
	}
	if code := post(t, second); code != http.StatusOK {
		t.Fatalf("current URL: %d, want 200", code)
	}
	if st, by := status(t); st != "approved" || by != "external-callback" {
		t.Fatalf("waitpoint = (%s, %q), want (approved, external-callback)", st, by)
	}
	var minted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'waitpoint.callback_token.minted' AND entity_id = ?`, tok).Scan(&minted); err != nil || minted != 2 {
		t.Fatalf("mint audit rows = %d (%v), want 2", minted, err)
	}

	// 5. A decided waitpoint mints nothing.
	if out, err := runWaitpointDeciderCLI(t, cfgPath, "routine", "waitpoints", "callback-url", tok); err == nil {
		t.Fatalf("callback-url on a decided waitpoint succeeded:\n%s", out)
	}
}
