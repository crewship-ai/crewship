package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func crewGrantRequest(user, workspace, method, path string, body []byte) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: user})
	return r.WithContext(context.WithValue(ctx, ctxWorkspaceID, workspace))
}

func TestCrewGrantDirectoryLeakAndQueryBound(t *testing.T) {
	mdb := testutil.MigratedDB(t)
	db := mdb.DB
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('crew-guest','crew-guest@test.invalid'),('ungranted','ungranted@test.invalid')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('crew-guest',?,'crew-guest','VIEWER','restricted'),('ungranted',?,'ungranted','VIEWER','restricted')`, workspace, workspace)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('foreign-w','Foreign','foreign-w')`)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('shared',?,'Shared','shared'),('hidden',?,'HIDDEN-CREW-CANARY','hidden'),('foreign','foreign-w','FOREIGN-CREW-CANARY','foreign')`, workspace, workspace)
	seedAgentRow(t, db, "shared-0", workspace, "shared", "Visible", "shared-0", "AGENT")
	seedAgentRow(t, db, "hidden-agent", workspace, "hidden", "HIDDEN-AGENT-CANARY", "hidden-agent", "AGENT")
	seedAgentRow(t, db, "foreign-agent", "foreign-w", "foreign", "FOREIGN-AGENT-CANARY", "foreign-agent", "AGENT")
	store := access.Store{DB: db}
	m, err := store.Membership(t.Context(), "crew-guest", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "crew-guest", workspace, "restricted", m, []access.Right{{Kind: "crew", ID: "shared", Operation: "view"}, {Kind: "crew", ID: "shared", Operation: "use"}, {Kind: "agent", ID: "shared-0", Operation: "discover"}}); err != nil {
		t.Fatal(err)
	}
	counted, counter := testutil.CountingSQLDB(t, mdb.Path(), 1)
	read := func(user, id string, want, count int) {
		t.Helper()
		r := crewGrantRequest(user, workspace, "GET", "/api/v1/agents", nil)
		r.SetPathValue("agentId", id)
		rr := httptest.NewRecorder()
		counter.Reset()
		if !restrictedDirectory(counted, rr, r) || rr.Code != want {
			t.Fatalf("directory: %d %s", rr.Code, rr.Body.String())
		}
		if got := counter.Count(); got != 2 {
			t.Fatalf("directory query count=%d, want2", got)
		}
		for _, canary := range []string{"HIDDEN-", "FOREIGN-", "hidden-agent", "foreign-agent", "system_prompt", "container_id"} {
			if strings.Contains(rr.Body.String(), canary) {
				t.Fatalf("directory leaked %s", canary)
			}
		}
		if id == "" {
			var rows []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != count {
				t.Fatalf("duplicate/missing agents: got%d want%d", len(rows), count)
			}
			seen := map[string]bool{}
			for _, row := range rows {
				id := row["id"].(string)
				if seen[id] {
					t.Fatal("duplicate agent")
				}
				seen[id] = true
			}
		}
	}
	read("crew-guest", "", 200, 1)
	for i := 1; i < 31; i++ {
		id := fmt.Sprintf("shared-%d", i)
		seedAgentRow(t, db, id, workspace, "shared", id, id, "AGENT")
	}
	read("crew-guest", "", 200, 31)
	read("crew-guest", "shared-0", 200, 0)
	read("crew-guest", "hidden-agent", 404, 0)
	read("ungranted", "", 200, 0)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := memberDepartureSnapshot(t.Context(), tx, workspace, "crew-guest")
	_ = tx.Rollback()
	if err != nil {
		t.Fatal("crew grants broke departure audit:", err)
	}
	data, _ := json.Marshal(snapshot)
	if !bytes.Contains(data, []byte(`"kind":"crew"`)) {
		t.Fatal("departure lost crew provenance")
	}
	execOrFatal(t, db, `DELETE FROM access_grants WHERE member_id='crew-guest' AND resource_kind='crew'`)
	read("crew-guest", "", 200, 1)
}

func TestCrewGrantEffectiveAccessBulkQueryBound(t *testing.T) {
	mdb := testutil.MigratedDB(t)
	db := mdb.DB
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('crew-guest','crew-guest@test.invalid')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('crew-guest',?,'crew-guest','VIEWER','restricted')`, workspace)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('hidden-crew',?,'UNGRANTED-CREW-CANARY','hidden-crew')`, workspace)
	counted, counter := testutil.CountingSQLDB(t, mdb.Path(), 1)
	h := &WorkspaceHandler{db: counted, logger: newTestLogger()}
	store := access.Store{DB: db}
	rights := []access.Right{}
	read := func(want int) int64 {
		t.Helper()
		r := crewGrantRequest("crew-guest", workspace, "GET", "/api/v1/workspaces/"+workspace+"/access/me", nil)
		r.SetPathValue("workspaceId", workspace)
		rr := httptest.NewRecorder()
		counter.Reset()
		h.GetOwnEffectiveAccess(rr, r)
		if rr.Code != 200 {
			t.Fatalf("effective access: %d %s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "UNGRANTED-CREW-CANARY") {
			t.Fatal("ungranted crew name leaked")
		}
		var got memberEffectiveAccess
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Decisions) != want {
			t.Fatalf("decisions%d want%d", len(got.Decisions), want)
		}
		for _, d := range got.Decisions {
			if d.State != "allowed" || !strings.HasPrefix(d.Name, "Visible crew ") {
				t.Fatalf("wrong crew decision: %+v", d)
			}
		}
		return counter.Count()
	}
	var baseline int64
	for i := 0; i < 31; i++ {
		id := fmt.Sprintf("crew-%d", i)
		execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES(?,?,?,?)`, id, workspace, fmt.Sprintf("Visible crew %d", i), id)
		rights = append(rights, access.Right{Kind: "crew", ID: id, Operation: "view"})
		if i != 0 && i != 30 {
			continue
		}
		m, err := store.Membership(t.Context(), "crew-guest", workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, "crew-guest", workspace, "restricted", m, rights); err != nil {
			t.Fatal(err)
		}
		read(i + 1)
		got := read(i + 1)
		if i == 0 {
			baseline = got
		} else if got != baseline || got > 20 {
			t.Fatalf("effective crew listing grew SQL: one=%d thirty-one=%d", baseline, got)
		}
	}
}

func TestCrewGrantMemberPolicyAPIAtomicRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('crew-guest','crew-guest@test.invalid')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('crew-guest',?,'crew-guest','VIEWER','restricted')`, workspace)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('shared',?,'Shared','shared')`, workspace)
	store := access.Store{DB: db}
	p, err := store.Policy(t.Context(), owner, "crew-guest", workspace)
	if err != nil {
		t.Fatal(err)
	}
	h := &WorkspaceHandler{db: db, logger: newTestLogger()}
	for _, rights := range [][]access.Right{{{Kind: "crew", ID: "shared", Operation: "use"}}, {}} {
		p.Rights = rights
		p.GrantDetails = nil
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		r := crewGrantRequest(owner, workspace, "PUT", "/", data)
		r.SetPathValue("workspaceId", workspace)
		r.SetPathValue("memberId", "crew-guest")
		rr := httptest.NewRecorder()
		h.PutMemberAccess(rr, r)
		if rr.Code != 200 {
			t.Fatalf("policy PUT: %d %s", rr.Code, rr.Body.String())
		}
		var got access.Policy
		if err = json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Rights) != len(rights) || got.Revision <= p.Revision {
			t.Fatal("lost crew policy or revision")
		}
		p = got
	}
}
