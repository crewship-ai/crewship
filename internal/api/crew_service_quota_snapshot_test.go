package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
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

func TestCrewServiceUpdateCannotProbePrivateSettings(t *testing.T) {
	reencSetV1(t)
	const private = `[{"name":"redis","image":"redis:7","env":{"CACHE_PASSWORD":"PRIVATE_CANARY"}}]`
	sealed, err := serviceconfig.Seal(private)
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []struct{ name, value string }{{"legacy", private}, {"encrypted", sealed}} {
		t.Run(stored.name, func(t *testing.T) {
			h, db, user, ws := covCruNewCrew(t)
			seedCrewRow(t, db, "private-probe", ws, "Private", "private-probe")
			if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, stored.value, "private-probe"); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/private-probe", nil)
			req.SetPathValue("crewId", "private-probe")
			rr := httptest.NewRecorder()
			h.Get(rr, withWorkspaceUser(req, user, ws, "OWNER"))
			var read map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &read); err != nil {
				t.Fatal(err)
			}
			if rr.Code != http.StatusOK || read["services_json"] != serviceconfig.Redacted {
				t.Fatalf("expected withheld configuration: %d %s", rr.Code, rr.Body.String())
			}
			for _, role := range []string{"OWNER", "ADMIN", "MEMBER", "VIEWER"} {
				t.Run(role, func(t *testing.T) {
					want := http.StatusConflict
					if role == "MEMBER" || role == "VIEWER" {
						want = http.StatusForbidden
					}
					var first string
					for _, guess := range []string{private, strings.ReplaceAll(private, "PRIVATE_CANARY", "WRONG_GUESS"), serviceconfig.Redacted, ""} {
						raw, _ := json.Marshal(map[string]string{"services_json": guess, "name": "Must not change"})
						rr := covCruDoUpdate(h, "private-probe", user, ws, role, string(raw))
						if rr.Code != want || (first != "" && rr.Body.String() != first) {
							t.Fatalf("guess distinguished or accepted: %d %s", rr.Code, rr.Body.String())
						}
						first = rr.Body.String()
					}
				})
			}
			var value, name string
			if err := db.QueryRow(`SELECT services_json, name FROM crews WHERE id=?`, "private-probe").Scan(&value, &name); err != nil {
				t.Fatal(err)
			}
			if value != stored.value || name != "Private" {
				t.Fatal("rejected update changed crew")
			}
			// Retrying an unrelated patch is safe when withheld services are omitted.
			for i := 0; i < 2; i++ {
				rr := covCruDoUpdate(h, "private-probe", user, ws, "OWNER", `{"name":"Renamed"}`)
				if rr.Code != http.StatusOK {
					t.Fatalf("unrelated patch: %d %s", rr.Code, rr.Body.String())
				}
			}
			if err := db.QueryRow(`SELECT services_json FROM crews WHERE id=?`, "private-probe").Scan(&value); err != nil {
				t.Fatal(err)
			}
			if value != stored.value {
				t.Fatal("unrelated patch replaced private settings")
			}
		})
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

// The snapshot check must not reveal redacted settings: for a private
// configuration a correct and a wrong expected_services_json get the same
// answer, decided before the snapshot is ever compared.
func TestCrewServiceQuotaSnapshotIsNotAPrivateSettingsOracle(t *testing.T) {
	h, db, user, ws := covCruNewCrew(t)
	seedCrewRow(t, db, "quota-oracle", ws, "Private", "private-oracle")
	private := `[{"name":"redis","image":"redis:7","env":{"CACHE_PASSWORD":"PRIVATE_CANARY"}}]`
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, private, "quota-oracle"); err != nil {
		t.Fatal(err)
	}
	answer := func(expected string) (int, string) {
		raw, _ := json.Marshal(map[string]any{"services_json": `[{"name":"redis","image":"redis:7","quota_enforced":true}]`, "expected_services_json": expected})
		rr := covCruDoUpdate(h, "quota-oracle", user, ws, "OWNER", string(raw))
		return rr.Code, rr.Body.String()
	}
	rightCode, rightBody := answer(private)
	wrongCode, wrongBody := answer(`[{"name":"redis","image":"redis:7","env":{"CACHE_PASSWORD":"WRONG_GUESS"}}]`)
	if rightCode != http.StatusConflict || rightCode != wrongCode || rightBody != wrongBody {
		t.Fatalf("redacted settings distinguishable by guess: right %d %s / wrong %d %s", rightCode, rightBody, wrongCode, wrongBody)
	}
}
