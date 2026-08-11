package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// A port exposure's token IS its authorization — /exposed/{token}/... runs
// with no authentication at all. Hashing it at rest (#1888) only pays off if
// the cleartext never reaches disk in the first place.
//
// The obvious implementation writes the cleartext on INSERT and lets the
// registry redact it a moment later. That leaves a live capability token in
// the database file for the remainder of the request — long enough for a
// crash, a WAL checkpoint or a backup to capture it, which is the exposure
// hashing exists to remove. This test pins the stronger property: it is never
// there at all.
//
// ISOLATING THE INSERT IS THE WHOLE TRICK. Reading the row after a normal
// request proves nothing: registry.Add redacts it microseconds later, so the
// end state is identical whether the INSERT wrote the secret or the marker.
// (A first draft of this test did exactly that and passed against the
// unfixed code.) The registry here is built with a nil database, so its
// redacting UPDATE is a no-op and the row shows only what the INSERT itself
// wrote — which is also a faithful stand-in for "the process died before Add
// ran".
func TestRequestExpose_NeverWritesTheCleartextToken(t *testing.T) {
	t.Parallel()
	h, db := covPXHandler(t, AllowAllPolicy{}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	// h.db keeps the real database for the INSERT; only the registry loses
	// its handle, which is what disables the follow-up redaction.
	h.registry = NewPortExposeRegistry(nil, newTestLogger())

	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != 200 && rr.Code != 201 {
		t.Fatalf("RequestExpose = %d: %s", rr.Code, rr.Body.String())
	}

	// The caller still gets a usable capability URL — the point is that the
	// secret lives in the response and in memory, not in the table.
	var resp struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	secret := resp.Token
	if secret == "" {
		// Some shapes return only the URL; recover the token from it so the
		// assertion below still has something real to look for.
		if i := strings.Index(resp.URL, "/exposed/"); i >= 0 {
			secret = strings.Trim(strings.TrimPrefix(resp.URL[i:], "/exposed/"), "/")
		}
	}
	if secret == "" {
		t.Fatalf("no token or /exposed/ URL in the response: %s", rr.Body.String())
	}

	// token_hash is not selected: this fixture builds port_exposures without
	// it (the registry adds it at runtime, and the registry is disabled
	// here). That absence is itself the reason the INSERT must not name the
	// column — an INSERT listing a column some schemas lack would fail on
	// exactly these fixtures.
	var stored string
	if err := db.QueryRow(`SELECT token FROM port_exposures`).Scan(&stored); err != nil {
		t.Fatalf("read row: %v", err)
	}

	if stored == secret {
		t.Error("the cleartext capability token is stored in port_exposures.token — anyone who reads the database file has a live URL")
	}
	if !strings.HasPrefix(stored, "redacted:") {
		t.Errorf("port_exposures.token = %q, want the spent marker; the column is NOT NULL UNIQUE so it cannot simply be emptied", stored)
	}
	// That token_hash IS written on the normal path is covered by
	// TestPortExposeToken_AddRedactsTheRowItJustIndexed; the registry that
	// writes it is deliberately disabled here.

	// And the secret must not have leaked into some other column of the row
	// either. Asserting only on `token` would pass a change that moved the
	// cleartext one column sideways.
	rows, err := db.Query(`SELECT * FROM port_exposures`)
	if err != nil {
		t.Fatalf("select *: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if !rows.Next() {
		t.Fatal("no row")
	}
	cells := make([]any, len(cols))
	for i := range cells {
		cells[i] = new(any)
	}
	if err := rows.Scan(cells...); err != nil {
		t.Fatalf("scan: %v", err)
	}
	for i, c := range cells {
		v, _ := (*(c.(*any))).(string)
		if v != "" && strings.Contains(v, secret) {
			t.Errorf("column %q holds the cleartext token", cols[i])
		}
	}
}
