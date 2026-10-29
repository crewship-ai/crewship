package mooring

import "testing"

// Independent review: Gridcraft presence is a canonical page-grant liveness
// dependency in pages_grants_authz.go. The admission callback remains synthetic;
// this test isolates unavoidable SQL invalidation, not actual app admission.
// Private-audience custody already denies a mere crew recipient; no live-reader
// bypass is claimed from the changed grant verdict alone.
func TestP3AuthorityGridcraftAttachmentClosesIssuedGate(t *testing.T) {
	s, db, _, owner := deliveryFixture(t)
	for _, statement := range []string{
		`UPDATE pages SET private_audience=1 WHERE id='p'`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('issuer-member','w','u','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('recipient-crew','w','Recipient','recipient')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('p','crew','recipient-crew','write','u')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// With an active OWNER issuer, the canonical reader's first Gridcraft CASE
	// controls this grant: crew write grants become inert on workbook attachment.
	inert := func() bool {
		t.Helper()
		var v bool
		if err := db.QueryRow(`SELECT (g.level='write' AND EXISTS(SELECT 1 FROM gridcraft_heads gh WHERE gh.page_id=p.id) AND (g.subject_type<>'user' OR p.owner_user_id IS NULL OR g.granted_by_user_id<>p.owner_user_id)) FROM page_grants g JOIN pages p ON p.id=g.page_id WHERE g.page_id='p'`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if inert() {
		t.Fatal("fixture already inert")
	}
	grant := deliveryOpen(t, s, owner, "human-a", "before-workbook-attachment")
	var before, after int64
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='w'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('p','w',1,1,1,X'00')`); err != nil {
		t.Fatal(err)
	}
	if !inert() {
		t.Fatal("canonical grant liveness did not change")
	}
	var closed bool
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='w'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, grant.GateID).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if !closed || after <= before {
		t.Fatalf("canonical grant cut escaped epoch/gate: before=%d after=%d closed=%v", before, after, closed)
	}
}
