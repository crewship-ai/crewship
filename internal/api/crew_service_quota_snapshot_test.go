package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCrewServiceQuotaClientSnapshot(t *testing.T) {
	h, db, user, ws := covCruNewCrew(t)
	seedCrewRow(t, db, "quota-snapshot", ws, "Quota service", "quota-service")
	old := `[{"name":"redis","image":"redis:7","volumes":[{"name":"data","mount":"/data"}]}]`
	current := `[{"name":"redis","image":"redis:8","env_refs":["CACHE_TOKEN"],"volumes":[{"name":"data","mount":"/data"}]}]`
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, current, "quota-snapshot"); err != nil {
		t.Fatal(err)
	}
	updated := `[{"name":"redis","image":"redis:8","env_refs":["CACHE_TOKEN"],"quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":67108864,"generation":1}]}]`
	body := func(expected *string) string {
		req := map[string]any{"services_json": updated}
		if expected != nil {
			req["expected_services_json"] = *expected
		}
		raw, _ := json.Marshal(req)
		return string(raw)
	}
	rr := covCruDoUpdate(h, "quota-snapshot", user, ws, "OWNER", body(&old))
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale client: %d %s", rr.Code, rr.Body.String())
	}
	var stored string
	if err := db.QueryRow(`SELECT services_json FROM crews WHERE id=?`, "quota-snapshot").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != current {
		t.Fatal("stale disk edit replaced newer image or credentials")
	}
	rr = covCruDoUpdate(h, "quota-snapshot", user, ws, "OWNER", body(&current))
	if rr.Code != http.StatusOK {
		t.Fatalf("matching client: %d %s", rr.Code, rr.Body.String())
	}
	rr = covCruDoUpdate(h, "quota-snapshot", user, ws, "OWNER", body(nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("legacy caller lost compatibility: %d %s", rr.Code, rr.Body.String())
	}
}
func TestCrewServiceQuotaSnapshotCannotReplacePrivateSettings(t *testing.T) {
	h, db, user, ws := covCruNewCrew(t)
	seedCrewRow(t, db, "quota-private", ws, "Private", "private")
	private := `[{"name":"redis","image":"redis:7","env":{"CACHE_PASSWORD":"PRIVATE_CANARY"}}]`
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, private, "quota-private"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"services_json": `[{"name":"redis","image":"redis:7","quota_enforced":true}]`, "expected_services_json": private})
	rr := covCruDoUpdate(h, "quota-private", user, ws, "OWNER", string(raw))
	if rr.Code != http.StatusConflict {
		t.Fatalf("private settings replaced: %d %s", rr.Code, rr.Body.String())
	}
	var stored string
	if err := db.QueryRow(`SELECT services_json FROM crews WHERE id=?`, "quota-private").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != private {
		t.Fatal("private configuration changed")
	}
}
