package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A native Craft task's change of a Word document is audited like every other
// saved Document revision: one artifact.document_saved row on the saving
// transaction, naming the version, the agent, its run and the person, never
// the text. A change that leaves the document as it is records nothing.
func TestCraftNativeAgentTaskWordWriteIsAudited(t *testing.T) {
	f, m, _, call, _ := nativeAgentToolFixture(t)
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	count := func() int {
		var n int
		if err := f.rig.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.document_saved' AND entity_id='native-agent-page'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	same := m.config.AgentApply
	m.config.AgentApply = func(_ context.Context, _ CraftNativeDocumentProfile, native []byte, _ json.RawMessage) ([]byte, error) {
		return native, nil
	}
	if w := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"no-change","commands":[{"command":"text.insert","params":{"text":""}}]}`, lease.Generation)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if got := count(); got != 0 {
		t.Fatalf("an unchanged document was audited: %d", got)
	}
	m.config.AgentApply = same
	if w := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"edit","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, lease.Generation)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if got := count(); got != 1 {
		t.Fatalf("an agent's saved Word change has %d audit rows, want 1", got)
	}
	var user, meta string
	if err := f.rig.db.QueryRow(`SELECT COALESCE(user_id,''), metadata FROM audit_logs WHERE action='artifact.document_saved' AND entity_id='native-agent-page'`).Scan(&user, &meta); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal([]byte(meta), &fields)
	// The person is the one who started the task (its grant), the run the
	// task's confirmed run.
	if user != f.receipt.UserID || fields["version"] != "2" || fields["source"] != "agent" || fields["agent_id"] != f.a.AgentID || fields["run_id"] != f.a.RunID {
		t.Fatalf("audit row: user %q (want %q) run want %q metadata %s", user, f.receipt.UserID, f.a.RunID, meta)
	}
	if strings.Contains(meta, "agent edited") {
		t.Fatal("the audit row carries document text")
	}
	var sheet int
	_ = f.rig.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.sheet_saved' AND entity_id='native-agent-page'`).Scan(&sheet)
	if sheet != 0 {
		t.Fatal("a Word document was audited as a Sheet")
	}
}
