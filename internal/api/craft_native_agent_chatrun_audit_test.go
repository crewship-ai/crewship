package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// An agent's Sheet change is audited in the same transaction that saves it:
// one audit_logs row naming the page, the new version, the run and the person
// it acts for, never the cells. A refused change leaves none.
func TestChatRunSheetWriteIsAuditedWithItsVersion(t *testing.T) {
	r := newChatRunToolRig(t)
	db := r.f.rig.db
	var page string
	if err := db.QueryRow(`SELECT id FROM pages WHERE slug='sheet-own'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	apply := func(base, request string) int {
		w := r.call(r.token, fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":%q,"request_id":%q,"commands":[{"command":"cell.set","params":{"cell":"A1","input":"secret-cell-text"}}]}`, lease.Generation, base, request))
		return w.Code
	}
	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.sheet_saved' AND entity_id=?`, page).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if code := apply("1", "first"); code != 200 {
		t.Fatalf("apply: %d", code)
	}
	if got := count(); got != 1 {
		t.Fatalf("an agent's saved change has %d audit rows, want 1", got)
	}
	var user, meta string
	if err := db.QueryRow(`SELECT COALESCE(user_id,''), metadata FROM audit_logs WHERE action='artifact.sheet_saved' AND entity_id=?`, page).Scan(&user, &meta); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(meta), &fields); err != nil {
		t.Fatalf("metadata: %v %s", err, meta)
	}
	sha, _ := fields["sha256"].(string)
	if user != r.a.HumanUserID || fields["version"] != "2" || fields["run_id"] != r.a.RunID || fields["source"] != "agent" || fields["agent_id"] != r.a.AgentID || fields["bytes"] == nil || len(sha) != 16 {
		t.Fatalf("audit row: user %q metadata %s", user, meta)
	}
	if strings.Contains(meta, "secret-cell-text") {
		t.Fatal("the audit row carries cell content")
	}
	// A stale base is refused before anything is saved: no row.
	if code := apply("1", "stale"); code == 200 {
		t.Fatal("a stale change was saved")
	}
	if got := count(); got != 1 {
		t.Fatalf("a refused change was audited: %d rows", got)
	}
}
