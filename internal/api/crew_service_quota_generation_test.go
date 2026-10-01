package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A generation's capacity is physically fixed. An edit that changes
// quota_bytes without a generation bump could never start, so it is
// rejected at save time with the fix in the message, and nothing is stored.
func TestCrewUpdateRejectsQuotaResizeWithoutGenerationBump(t *testing.T) {
	stored := `[{"name":"database","image":"postgres:16","quota_enforced":true,"volumes":[{"name":"data","mount":"/var/lib/postgresql/data","quota_bytes":67108864,"generation":1}]}]`
	cases := []struct {
		name     string
		next     string
		wantCode int
	}{
		{name: "resize without bump", next: strings.Replace(stored, "67108864", "134217728", 1), wantCode: http.StatusBadRequest},
		{name: "resize with bump", next: strings.Replace(strings.Replace(stored, "67108864", "134217728", 1), `"generation":1`, `"generation":2`, 1), wantCode: http.StatusOK},
		{name: "unchanged capacity", next: strings.Replace(stored, "postgres:16", "postgres:17", 1), wantCode: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, user, ws := covCruNewCrew(t)
			seedCrewRow(t, db, "quota-gen", ws, "Quota", "quota-gen")
			if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, stored, "quota-gen"); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"services_json": tc.next})
			rr := covCruDoUpdate(h, "quota-gen", user, ws, "OWNER", string(raw))
			if rr.Code != tc.wantCode {
				t.Fatalf("code %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode != http.StatusBadRequest {
				return
			}
			if !strings.Contains(rr.Body.String(), "generation 2") {
				t.Fatalf("error does not say how to fix it: %s", rr.Body.String())
			}
			var got string
			if err := db.QueryRow(`SELECT services_json FROM crews WHERE id=?`, "quota-gen").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != stored {
				t.Fatal("rejected resize was stored")
			}
		})
	}
}
