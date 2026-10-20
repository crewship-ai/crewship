package api

import "testing"

// Personal placement is explicit metadata, never inferred from human ownership
// or an absent folder. Existing catalog authorization remains authoritative.
func TestPagesPersonalSpaceClassification(t *testing.T) {
	h, ws, author, _, create := documentCreationFixture(t)
	creationExpect(t, create("member", documentCreationBody), 201)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('legacy-personal',?,'legacy-personal','Legacy',?,'{}')`, ws, author)
	for _, tc := range []struct {
		user, role     string
		privateVisible bool
	}{
		{author, "MEMBER", true}, {"space-owner", "OWNER", true}, {"space-admin", "ADMIN", false},
	} {
		if tc.user != author {
			pagesSeedUser(t, h, ws, tc.user, tc.user+"@example.test", tc.role)
		}
		rows := pagesListRows(t, h, ws, tc.user, tc.role)
		if row, ok := rows["created-doc"]; ok != tc.privateVisible {
			t.Errorf("%s private visible=%v", tc.role, ok)
		} else if ok && row["private_audience"] != true {
			t.Errorf("%s private marker=%v", tc.role, row["private_audience"])
		}
		if row := rows["legacy-personal"]; row == nil || row["private_audience"] != false {
			t.Errorf("%s legacy marker=%v", tc.role, row)
		}
	}
}
