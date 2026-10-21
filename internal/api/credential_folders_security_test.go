package api

// Security regressions for Vault folders (#3706), from an independent review
// of PR #3697: a folder move or a floor change must never loosen the
// protection a credential is held to.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func (g *credFolderRig) patchFolder(t *testing.T, role, id, body string) int {
	t.Helper()
	rr := httptest.NewRecorder()
	req := g.req("PATCH", "/api/v1/credential-folders/"+id, role, body)
	req.SetPathValue("folderId", id)
	g.h.Update(rr, req)
	return rr.Code
}

func (g *credFolderRig) credLevel(t *testing.T, id string) int {
	t.Helper()
	var level int
	if err := g.cred.db.QueryRow(`SELECT security_level FROM credentials WHERE id = ?`, id).Scan(&level); err != nil {
		t.Fatal(err)
	}
	return level
}

func (g *credFolderRig) setCredLevel(t *testing.T, id string, level int) {
	t.Helper()
	if _, err := g.cred.db.Exec(`UPDATE credentials SET security_level = ? WHERE id = ?`, level, id); err != nil {
		t.Fatal(err)
	}
}

// The reviewer's reproduction, kept verbatim in substance.
func TestAuditCredentialFolder_ManagerCannotRemoveInheritedRestriction(t *testing.T) {
	g := newCredFolderRig(t)
	code, parent := g.create(t, "OWNER", `{"name":"Protected","reveal_policy":"nobody"}`)
	if code != http.StatusCreated {
		t.Fatalf("parent create: %d", code)
	}
	code, child := g.create(t, "OWNER", `{"name":"Child","parent_id":"`+parent.ID+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("child create: %d", code)
	}
	g.seedCred(t, "audit-secret", "synthetic-value", child.ID)
	before, err := loadFolderPolicy(context.Background(), g.cred.db, g.ws, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := g.req("PATCH", "/api/v1/credential-folders/"+child.ID, "MANAGER", `{"parent_id":null}`)
	req.SetPathValue("folderId", child.ID)
	g.h.Update(rr, req)
	after, err := loadFolderPolicy(context.Background(), g.cred.db, g.ws, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("MANAGER reparent status=%d; effective reveal changed %s -> %s; want 403", rr.Code, before.revealPolicy, after.revealPolicy)
	}
}

// The reviewer's reproduction, kept verbatim in substance.
func TestAuditCredentialFolder_RaisingFloorPreservesMemberInvariant(t *testing.T) {
	g := newCredFolderRig(t)
	id := g.mustRoot(t, "Floor")
	g.seedCred(t, "audit-tier-secret", "synthetic-value", id)
	rr := httptest.NewRecorder()
	req := g.req("PATCH", "/api/v1/credential-folders/"+id, "ADMIN", `{"min_security_level":4}`)
	req.SetPathValue("folderId", id)
	g.h.Update(rr, req)
	if rr.Code != http.StatusOK {
		return // Refusing an incompatible change is safe too.
	}
	p, err := loadFolderPolicy(context.Background(), g.cred.db, g.ws, id)
	if err != nil {
		t.Fatal(err)
	}
	var level int
	if err := g.cred.db.QueryRow(`SELECT security_level FROM credentials WHERE id='audit-tier-secret'`).Scan(&level); err != nil {
		t.Fatal(err)
	}
	if level < p.minLevel {
		t.Fatalf("folder floor=%d but existing credential security_level=%d", p.minLevel, level)
	}
}

// A folder move may tighten the effective policy at any role that may
// organise, but loosening it (reveal policy or tier floor) needs the role
// that could set the looser policy directly: OWNER or ADMIN.
func TestCredentialFolders_MoveMustNotLoosenEffectivePolicy(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		fromParent string // policy JSON of the current parent; "" = the folder is a root
		self       string // the moved folder's own policy JSON
		toParent   string // policy JSON of the target parent; "" = move to root
		want       int
	}{
		{"manager: child of sealed parent to root", "MANAGER", `"reveal_policy":"nobody"`, ``, "", http.StatusForbidden},
		{"manager: child of admins parent to root", "MANAGER", `"reveal_policy":"admins"`, ``, "", http.StatusForbidden},
		{"manager: child of floor-3 parent to root", "MANAGER", `"min_security_level":3`, ``, "", http.StatusForbidden},
		{"manager: child of sealed parent under plain parent", "MANAGER", `"reveal_policy":"nobody"`, ``, `"description":"plain"`, http.StatusForbidden},
		{"manager: child of sealed parent under admins parent", "MANAGER", `"reveal_policy":"nobody"`, ``, `"reveal_policy":"admins"`, http.StatusForbidden},
		{"manager: child of floor-4 parent under floor-2 parent", "MANAGER", `"min_security_level":4`, ``, `"min_security_level":2`, http.StatusForbidden},
		{"manager: child of plain parent to root", "MANAGER", `"description":"plain"`, ``, "", http.StatusOK},
		{"manager: child with its own seal to root keeps the seal", "MANAGER", `"reveal_policy":"nobody"`, `,"reveal_policy":"nobody"`, "", http.StatusOK},
		{"manager: plain root under sealed parent tightens", "MANAGER", "", ``, `"reveal_policy":"nobody"`, http.StatusOK},
		{"manager: child of admins parent under sealed parent tightens", "MANAGER", `"reveal_policy":"admins"`, ``, `"reveal_policy":"nobody"`, http.StatusOK},
		{"manager: floor-2 parent to floor-4 parent tightens", "MANAGER", `"min_security_level":2`, ``, `"min_security_level":4`, http.StatusOK},
		{"admin: child of sealed parent to root", "ADMIN", `"reveal_policy":"nobody"`, ``, "", http.StatusOK},
		{"owner: child of floor-3 parent under plain parent", "OWNER", `"min_security_level":3`, ``, `"description":"plain"`, http.StatusOK},
		{"member cannot move at all", "MEMBER", `"description":"plain"`, ``, "", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newCredFolderRig(t)
			parentField := ""
			if tc.fromParent != "" {
				code, p := g.create(t, "OWNER", `{"name":"From",`+tc.fromParent+`}`)
				if code != http.StatusCreated {
					t.Fatalf("from parent: %d", code)
				}
				parentField = `,"parent_id":"` + p.ID + `"`
			}
			code, f := g.create(t, "OWNER", `{"name":"Moved"`+parentField+tc.self+`}`)
			if code != http.StatusCreated {
				t.Fatalf("moved folder: %d", code)
			}
			body := `{"parent_id":null}`
			if tc.toParent != "" {
				code, to := g.create(t, "OWNER", `{"name":"To",`+tc.toParent+`}`)
				if code != http.StatusCreated {
					t.Fatalf("to parent: %d", code)
				}
				body = `{"parent_id":"` + to.ID + `"}`
			}
			before, err := loadFolderPolicy(context.Background(), g.cred.db, g.ws, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := g.patchFolder(t, tc.role, f.ID, body); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
			if tc.want != http.StatusOK {
				after, err := loadFolderPolicy(context.Background(), g.cred.db, g.ws, f.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.revealPolicy != before.revealPolicy || after.minLevel != before.minLevel {
					t.Fatalf("refused move still changed the effective policy: %s/L%d -> %s/L%d",
						before.revealPolicy, before.minLevel, after.revealPolicy, after.minLevel)
				}
			}
		})
	}
}

// Moving a CREDENTIAL out of a folder whose protection it only inherits is
// loosening too, and stays an OWNER/ADMIN decision.
func TestCredentialUpdate_MoveOutOfInheritedRestriction(t *testing.T) {
	tests := []struct {
		name   string
		role   string
		parent string // policy JSON of the parent of the folder the credential sits in
		target string // "root" = unfile, "plain" = a plain root folder
		want   int
	}{
		{"manager unfiles from child of sealed parent", "MANAGER", `"reveal_policy":"nobody"`, "root", http.StatusForbidden},
		{"manager moves from child of sealed parent to plain folder", "MANAGER", `"reveal_policy":"nobody"`, "plain", http.StatusForbidden},
		{"manager moves from child of floor-3 parent to plain folder", "MANAGER", `"min_security_level":3`, "plain", http.StatusForbidden},
		{"manager moves from child of plain parent to plain folder", "MANAGER", `"description":"plain"`, "plain", http.StatusOK},
		{"admin unfiles from child of sealed parent", "ADMIN", `"reveal_policy":"nobody"`, "root", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newCredFolderRig(t)
			_, p := g.create(t, "OWNER", `{"name":"Parent",`+tc.parent+`}`)
			_, child := g.create(t, "OWNER", `{"name":"Child","parent_id":"`+p.ID+`"}`)
			g.seedCred(t, "c1", "tok", child.ID)
			g.setCredLevel(t, "c1", 3)
			body := `{"folder_id":null}`
			if tc.target == "plain" {
				body = `{"folder_id":"` + g.mustRoot(t, "Plain") + `"}`
			}
			if code, _ := g.patchCred(t, tc.role, "c1", body); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

// Raising a floor raises the members it now binds, in the folder and in its
// subfolders, in the same write. Nothing is ever lowered.
func TestCredentialFolders_FloorRaiseLiftsExistingMembers(t *testing.T) {
	g := newCredFolderRig(t)
	_, root := g.create(t, "OWNER", `{"name":"Root"}`)
	_, kid := g.create(t, "OWNER", `{"name":"Kid","parent_id":"`+root.ID+`"}`)
	_, strict := g.create(t, "OWNER", `{"name":"Strict","parent_id":"`+root.ID+`","min_security_level":4}`)
	other := g.mustRoot(t, "Elsewhere")
	g.seedCred(t, "in-root", "a", root.ID)
	g.seedCred(t, "in-kid", "b", kid.ID)
	g.seedCred(t, "in-kid-high", "c", kid.ID)
	g.setCredLevel(t, "in-kid-high", 4)
	g.seedCred(t, "in-strict", "d", strict.ID)
	g.setCredLevel(t, "in-strict", 4)
	g.seedCred(t, "elsewhere", "e", other)

	if code := g.patchFolder(t, "ADMIN", root.ID, `{"min_security_level":3}`); code != http.StatusOK {
		t.Fatalf("raise floor = %d, want 200", code)
	}
	want := map[string]int{"in-root": 3, "in-kid": 3, "in-kid-high": 4, "in-strict": 4, "elsewhere": 1}
	for id, lvl := range want {
		if got := g.credLevel(t, id); got != lvl {
			t.Errorf("%s security_level = %d, want %d", id, got, lvl)
		}
	}
	// Lowering the floor again leaves the raised members where they are.
	if code := g.patchFolder(t, "ADMIN", root.ID, `{"min_security_level":1}`); code != http.StatusOK {
		t.Fatalf("lower floor = %d, want 200", code)
	}
	if got := g.credLevel(t, "in-root"); got != 3 {
		t.Errorf("lowering the floor changed a member: %d, want 3", got)
	}
}

func TestCredentialFolders_MoveUnderStricterParentLiftsMembers(t *testing.T) {
	for _, role := range []string{"MANAGER", "ADMIN"} {
		t.Run(role, func(t *testing.T) {
			g := newCredFolderRig(t)
			_, strict := g.create(t, "OWNER", `{"name":"Strict","min_security_level":3}`)
			moved := g.mustRoot(t, "Moved")
			g.seedCred(t, "c1", "tok", moved)
			if code := g.patchFolder(t, role, moved, `{"parent_id":"`+strict.ID+`"}`); code != http.StatusOK {
				t.Fatalf("move under stricter parent = %d, want 200", code)
			}
			if got := g.credLevel(t, "c1"); got != 3 {
				t.Fatalf("member security_level = %d, want 3", got)
			}
		})
	}
}

// Filing a credential into a folder with a higher floor raises it to the
// floor when the caller names no tier; a tier named below the floor is still
// refused, as on create.
func TestCredentialUpdate_MoveIntoFloorRaises(t *testing.T) {
	tests := []struct {
		name      string
		body      func(folder string) string
		want      int
		wantLevel int
	}{
		{"no tier named: raised to floor", func(f string) string { return `{"folder_id":"` + f + `"}` }, http.StatusOK, 3},
		{"tier named below floor: refused", func(f string) string { return `{"folder_id":"` + f + `","security_level":2}` }, http.StatusBadRequest, 1},
		{"tier named above floor: kept", func(f string) string { return `{"folder_id":"` + f + `","security_level":4}` }, http.StatusOK, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newCredFolderRig(t)
			_, locked := g.create(t, "OWNER", `{"name":"Locked","min_security_level":3}`)
			g.seedCred(t, "c1", "tok", "")
			code, c := g.patchCred(t, "ADMIN", "c1", tc.body(locked.ID))
			if code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
			if code == http.StatusOK && c.SecurityLevel != tc.wantLevel {
				t.Errorf("response security_level = %d, want %d", c.SecurityLevel, tc.wantLevel)
			}
			if got := g.credLevel(t, "c1"); got != tc.wantLevel {
				t.Errorf("stored security_level = %d, want %d", got, tc.wantLevel)
			}
		})
	}
}

// Defence in depth: the Keeper paths read max(credential tier, effective
// folder floor), so a row that somehow sits below its folder's floor (an old
// bundle, a hand edit) is still gated at the floor.
func TestCredentialEffectiveLevel_HonoursFolderFloor(t *testing.T) {
	g := newCredFolderRig(t)
	_, parent := g.create(t, "OWNER", `{"name":"P","min_security_level":4}`)
	_, kid := g.create(t, "OWNER", `{"name":"K","parent_id":"`+parent.ID+`"}`)
	_, mid := g.create(t, "OWNER", `{"name":"M","min_security_level":2}`)
	g.seedCred(t, "unfiled", "a", "")
	g.seedCred(t, "under-parent-floor", "b", kid.ID)
	g.seedCred(t, "own-floor", "c", mid.ID)
	g.seedCred(t, "above-floor", "d", mid.ID)
	g.setCredLevel(t, "above-floor", 3)

	tests := []struct {
		id   string
		want int
	}{
		{"unfiled", 1},
		{"under-parent-floor", 4},
		{"own-floor", 2},
		{"above-floor", 3},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			var got int
			if err := g.cred.db.QueryRow(`SELECT `+credentialEffectiveLevelSQL("c")+` FROM credentials c WHERE c.id = ?`, tc.id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("effective level = %d, want %d", got, tc.want)
			}
		})
	}
}
