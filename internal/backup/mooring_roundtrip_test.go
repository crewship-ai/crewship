package backup

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestMooringBackupPreservesConfirmedStateWithoutRunMandates(t *testing.T) {
	source := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, source, "mooring-roundtrip")
	other, _ := seedCovWorkspace(t, source, "mooring-foreign")
	seed := func(ws, page, owner, text string) {
		t.Helper()
		if _, e := source.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'OWNER')`, "member-"+page, ws, owner); e != nil {
			t.Fatal(e)
		}
		if _, e := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES(?,?,?,?,?,1,'{}')`, page, ws, page, "Generic", owner); e != nil {
			t.Fatal(e)
		}
		snapshot, _ := json.Marshal(map[string]string{"text": text})
		if _, e := source.Exec(`INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,?,'hello',1,1,?)`, ws, page, string(snapshot)); e != nil {
			t.Fatal(e)
		}
	}
	seed(workspace, "mooring-portable", "u_cov_mooring-roundtrip", "initial")
	seed(other, "mooring-foreign", "u_cov_mooring-foreign", "must not leak")
	tx, e := source.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	manifest, e := mooring.Application("hello")
	if e != nil {
		t.Fatal(e)
	}
	request := mooring.Request{MutationID: "confirmed", BaseRev: 1, Generation: 1, Command: "set", Arguments: json.RawMessage(`{"text":"Durable český content"}`)}
	receipt, e := (mooring.JSONStorage{}).Commit(t.Context(), tx, "mooring-portable", "user:u_cov_mooring-roundtrip", manifest, request)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = source.Exec(`INSERT INTO mooring_intents(page_id,mandate_id,run_id,human_user_id,page_version,policy_version,generation,base_rev,command,fingerprint) VALUES('mooring-portable','source-live-mandate','source-live-run','u_cov_mooring-roundtrip',1,1,1,2,'set','retained')`); e != nil {
		t.Fatal(e)
	}
	dump, e := DumpWorkspace(t.Context(), source, workspace)
	if e != nil {
		t.Fatal(e)
	}
	if len(dump.Tables["mooring_documents"]) != 1 || len(dump.Tables["mooring_receipts"]) != 1 || len(dump.Tables["mooring_intents"]) != 0 {
		t.Fatalf("confirmed content omitted or live authority leaked: documents=%d receipts=%d intents=%d", len(dump.Tables["mooring_documents"]), len(dump.Tables["mooring_receipts"]), len(dump.Tables["mooring_intents"]))
	}
	raw, e := json.Marshal(dump)
	if e != nil || strings.Contains(string(raw), "must not leak") || strings.Contains(string(raw), "source-live-run") {
		t.Fatal("backup crossed workspace or authority boundary", e)
	}
	destination := openMigratedDBCov(t)
	if e = RestoreDump(t.Context(), destination, dump); e != nil {
		t.Fatal(e)
	}
	var rev, generation, count int
	var snapshot, packet string
	if e = destination.QueryRow(`SELECT rev,generation,snapshot FROM mooring_documents WHERE page_id='mooring-portable'`).Scan(&rev, &generation, &snapshot); e != nil || rev != 2 || generation != 1 || !strings.Contains(snapshot, "Durable český content") {
		t.Fatalf("confirmed state lost: %d %d %s %v", rev, generation, snapshot, e)
	}
	if e = destination.QueryRow(`SELECT receipt FROM mooring_receipts WHERE page_id='mooring-portable' AND mutation_id='confirmed'`).Scan(&packet); e != nil {
		t.Fatal(e)
	}
	expected, _ := json.Marshal(receipt)
	if packet != string(expected) {
		t.Fatal("durable receipt changed on restore")
	}
	if e = destination.QueryRow(`SELECT COUNT(*) FROM mooring_intents`).Scan(&count); e != nil || count != 0 {
		t.Fatal("live mandate restored", count, e)
	}
	// Fresh authorization is still required by the host. Restored durable storage
	// keeps exact idempotence evidence rather than reapplying a confirmed command.
	tx, e = destination.BeginTx(t.Context(), &sql.TxOptions{})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	replayed, e := (mooring.JSONStorage{}).Commit(t.Context(), tx, "mooring-portable", "user:u_cov_mooring-roundtrip", manifest, request)
	if e != nil || replayed != receipt {
		t.Fatalf("confirmed mutation replay changed: %#v %v", replayed, e)
	}
	request.Arguments = json.RawMessage(`{"text":"different content"}`)
	if _, e = (mooring.JSONStorage{}).Commit(t.Context(), tx, "mooring-portable", "user:u_cov_mooring-roundtrip", manifest, request); e != mooring.ErrConflict {
		t.Fatalf("restored nonce accepted different payload: %v", e)
	}
	state, e := (mooring.JSONStorage{}).Read(t.Context(), tx, "mooring-portable")
	if e != nil || state.Rev != 2 || string(state.Snapshot) != snapshot {
		t.Fatalf("replay altered restored state: %#v %v", state, e)
	}
}
