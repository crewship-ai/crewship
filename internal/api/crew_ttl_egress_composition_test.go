package api

// #1662 landed on top of #1648, and both write to the crew response: the TTL
// work owns container_ttl_hours and the create/update handling around it; the
// capability work owns network_mode_enforced, its reason, and the warnings
// array that carries the advisory. They are independent by construction, but
// "independent by construction" is what every silently-broken rebase was
// before it broke. A crew that is BOTH on a non-enforcing provider AND has a
// TTL must report both, on the same response, with neither clobbering the
// other.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeCrewBody parses a handler response into the raw JSON object so the
// test sees exactly what a client would, keys and all.
func decodeCrewBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, rr.Body.String())
	}
	return body
}

func TestCreate_UnenforcedEgressAndExplicitTTL_BothReported(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	h.SetContainer(noEgressProvider{})

	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Ops","slug":"ops","network_mode":"restricted","container_ttl_hours":9}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	body := decodeCrewBody(t, rr)

	// #1662 side.
	if got := body["container_ttl_hours"]; got != float64(9) {
		t.Errorf("container_ttl_hours = %v, want 9", got)
	}
	var ttl sql.NullInt64
	if err := db.QueryRow(`SELECT container_ttl_hours FROM crews WHERE slug = 'ops'`).Scan(&ttl); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if !ttl.Valid || ttl.Int64 != 9 {
		t.Errorf("stored container_ttl_hours = %v (valid=%v), want 9", ttl.Int64, ttl.Valid)
	}

	// #1648 side, on the same response.
	if got := body["network_mode_enforced"]; got != false {
		t.Errorf("network_mode_enforced = %v, want false on a provider that drops the mode", got)
	}
	if reason, _ := body["network_mode_unenforced_reason"].(string); reason == "" {
		t.Error("network_mode_unenforced_reason is empty on an unenforced crew")
	}
	warnings, ok := body["warnings"].([]any)
	if !ok || len(warnings) == 0 {
		t.Fatalf("warnings = %#v, want the egress advisory", body["warnings"])
	}
	var found bool
	for _, w := range warnings {
		if s, _ := w.(string); strings.Contains(strings.ToLower(s), "enforc") {
			found = true
		}
	}
	if !found {
		t.Errorf("no egress advisory in warnings: %v", warnings)
	}
}

func TestCreate_UnenforcedEgressAndNeverStopTTL_NeitherAnnotationClobbersTheOther(t *testing.T) {
	// The explicit-0 case is the one the TTL change altered, so it is the one
	// most likely to have been lost in a merge: 0 must reach the row (not
	// become NULL, which now means the server default) AND the crew must
	// still be reported as unenforced.
	h, db, userID, wsID := covCruNewCrew(t)
	h.SetContainer(noEgressProvider{})

	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Ops","slug":"ops","network_mode":"restricted","container_ttl_hours":0}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	body := decodeCrewBody(t, rr)

	if got := body["container_ttl_hours"]; got != float64(0) {
		t.Errorf("container_ttl_hours = %v, want a reported 0 (never stop)", got)
	}
	var ttl sql.NullInt64
	if err := db.QueryRow(`SELECT container_ttl_hours FROM crews WHERE slug = 'ops'`).Scan(&ttl); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if !ttl.Valid || ttl.Int64 != 0 {
		t.Errorf("stored container_ttl_hours = %v (valid=%v), want a stored 0", ttl.Int64, ttl.Valid)
	}
	if got := body["network_mode_enforced"]; got != false {
		t.Errorf("network_mode_enforced = %v, want false", got)
	}
}

func TestCreate_EnforcingProviderAndNullTTL_ReportsEnforcedAndNoTTL(t *testing.T) {
	// The other corner: a provider that honours the mode, and a crew that
	// says nothing about its TTL. network_mode_enforced must still be
	// PRESENT and true (an absent key reads as "yes" for the wrong reason),
	// and container_ttl_hours must be null so the server default applies.
	h, db, userID, wsID := covCruNewCrew(t)
	h.SetContainer(egressFakeProvider{})

	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Ops","slug":"ops","network_mode":"restricted"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	body := decodeCrewBody(t, rr)

	enforced, present := body["network_mode_enforced"]
	if !present {
		t.Fatal("network_mode_enforced absent; a client would read that as enforced by default")
	}
	if enforced != true {
		t.Errorf("network_mode_enforced = %v, want true", enforced)
	}
	if got, present := body["container_ttl_hours"]; !present || got != nil {
		t.Errorf("container_ttl_hours = %v (present=%v), want a present null", got, present)
	}
	var ttl sql.NullInt64
	if err := db.QueryRow(`SELECT container_ttl_hours FROM crews WHERE slug = 'ops'`).Scan(&ttl); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if ttl.Valid {
		t.Errorf("stored container_ttl_hours = %d, want NULL so the server default applies", ttl.Int64)
	}
}

func TestUpdate_UnenforcedEgressAndTTLPatch_BothReported(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	h.SetContainer(noEgressProvider{})
	seedRestrictedCrew(t, db, "cr-both", wsID, "both")

	rr := covCruDoUpdate(h, "cr-both", userID, wsID, "OWNER", `{"container_ttl_hours":0}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	body := decodeCrewBody(t, rr)

	if got := body["container_ttl_hours"]; got != float64(0) {
		t.Errorf("container_ttl_hours = %v, want 0", got)
	}
	if got := body["network_mode_enforced"]; got != false {
		t.Errorf("network_mode_enforced = %v, want false", got)
	}
	var ttl sql.NullInt64
	if err := db.QueryRow(`SELECT container_ttl_hours FROM crews WHERE id = 'cr-both'`).Scan(&ttl); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if !ttl.Valid || ttl.Int64 != 0 {
		t.Errorf("stored container_ttl_hours = %v (valid=%v), want a stored 0", ttl.Int64, ttl.Valid)
	}
}

func TestGet_UnenforcedEgressAndTTL_BothOnTheReadPath(t *testing.T) {
	// The read path is a different construction site from create/update, and
	// it is the one `crewship crew get` renders.
	h, db, userID, wsID := covCruNewCrew(t)
	h.SetContainer(noEgressProvider{})
	seedRestrictedCrew(t, db, "cr-get", wsID, "getboth")
	if _, err := db.Exec(`UPDATE crews SET container_ttl_hours = 7 WHERE id = 'cr-get'`); err != nil {
		t.Fatalf("set ttl: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/crews/cr-get", bytes.NewReader(nil))
	req.SetPathValue("crewId", "cr-get")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	body := decodeCrewBody(t, rr)

	if got := body["container_ttl_hours"]; got != float64(7) {
		t.Errorf("container_ttl_hours = %v, want 7", got)
	}
	if got := body["network_mode_enforced"]; got != false {
		t.Errorf("network_mode_enforced = %v, want false", got)
	}
	if reason, _ := body["network_mode_unenforced_reason"].(string); reason == "" {
		t.Error("network_mode_unenforced_reason is empty on the read path")
	}
}
