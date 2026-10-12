package pipeline

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

const authorityPageSpec = `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"slug":"dashboard"},"spec":{"name":"Dashboard","panels":[{"id":"panel","schema":"status.v1","owner":"crew/team","actions":[{"id":"run","kind":"call","label":"Run","routine":"recipe","params":{"task":"fixed"}}]}]}}`

func pageActionBoundaryRig(t *testing.T) (*sql.DB, PageActionInvocation) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
 CREATE TABLE workspace_members(user_id TEXT,workspace_id TEXT,role TEXT);
 INSERT INTO workspace_members VALUES('user','ws','OWNER');
 CREATE TABLE crews(id TEXT,workspace_id TEXT,deleted_at TEXT);
 INSERT INTO crews VALUES('crew','ws',NULL);
 CREATE TABLE crew_members(crew_id TEXT,user_id TEXT);
 CREATE TABLE pipelines(id TEXT,workspace_id TEXT,slug TEXT,status TEXT,deleted_at TEXT);
 INSERT INTO pipelines VALUES('recipe','ws','recipe','active',NULL);
 CREATE TABLE pages(id TEXT,workspace_id TEXT,spec_json TEXT);
 CREATE TABLE page_panels(page_id TEXT,panel_id TEXT,owner_crew_id TEXT);
 INSERT INTO page_panels VALUES('page','panel','crew');
 CREATE TABLE page_project_live(page_id TEXT,version INTEGER,published INTEGER);
 CREATE TABLE page_project_publications(page_id TEXT,version INTEGER,spec_json TEXT);
 INSERT INTO pages VALUES('page','ws',?);
 INSERT INTO page_project_live VALUES('page',1,1);
 INSERT INTO page_project_publications VALUES('page',1,?);`, authorityPageSpec, authorityPageSpec)
	if err != nil {
		t.Fatal(err)
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(authorityPageSpec), &doc); err != nil {
		t.Fatal(err)
	}
	panel, ok := doc.FindPanel("panel")
	if !ok {
		t.Fatal("fixture panel absent")
	}
	action, ok := panel.FindAction("run")
	if !ok {
		t.Fatal("fixture action absent")
	}
	return db, PageActionInvocation{PageID: "page", PanelID: "panel", ActionID: "run", PipelineID: "recipe", ActionDigest: PageActionDigest(action)}
}

func TestDeclaredPageActionRequiresBothRoleFloorAndPanelVisibility(t *testing.T) {
	for _, tc := range []struct {
		role            string
		member, allowed bool
	}{
		{"OWNER", false, true}, {"ADMIN", false, true}, {"MANAGER", false, false}, {"MANAGER", true, true}, {"MEMBER", true, false}, {"VIEWER", true, false}, {"unknown", true, false},
	} {
		t.Run(tc.role+"/"+map[bool]string{true: "member", false: "outsider"}[tc.member], func(t *testing.T) {
			db, action := pageActionBoundaryRig(t)
			if _, err := db.Exec(`UPDATE workspace_members SET role=?`, tc.role); err != nil {
				t.Fatal(err)
			}
			if tc.member {
				if _, err := db.Exec(`INSERT INTO crew_members VALUES('crew','user')`); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckDeclaredPageAction(t.Context(), db, "user", "ws", action)
			if tc.allowed && err != nil {
				t.Fatalf("authorized action rejected: %v", err)
			}
			if !tc.allowed && !errors.Is(err, ErrInvocationAuthorityRevoked) {
				t.Fatalf("unauthorized action = %v", err)
			}
		})
	}
}

func TestPageActionRevocationCoversContractOwnershipAndPublicationChanges(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		args      []any
	}{
		{"deleted crew", `UPDATE crews SET deleted_at='now'`, nil},
		{"moved crew", `UPDATE crews SET workspace_id='other'`, nil},
		{"deleted recipe", `UPDATE pipelines SET deleted_at='now'`, nil},
		{"moved recipe", `UPDATE pipelines SET workspace_id='other'`, nil},
		{"disabled recipe", `UPDATE pipelines SET status='disabled'`, nil},
		{"renamed recipe", `UPDATE pipelines SET slug='changed'`, nil},
		{"removed panel", `DELETE FROM page_panels`, nil},
		{"removed declaration", `UPDATE pages SET spec_json='{}'`, nil},
		{"removed action", `UPDATE pages SET spec_json=?`, []any{strings.Replace(authorityPageSpec, `"id":"run"`, `"id":"different"`, 1)}},
		{"changed kind", `UPDATE pages SET spec_json=?`, []any{strings.Replace(authorityPageSpec, `"kind":"call"`, `"kind":"issue"`, 1)}},
		{"changed fixed params", `UPDATE pages SET spec_json=?`, []any{strings.Replace(authorityPageSpec, `"fixed"`, `"changed"`, 1)}},
		{"unpublished", `UPDATE page_project_live SET published=0`, nil},
		{"new publication", `UPDATE page_project_live SET version=2`, nil},
		{"different published spec", `UPDATE page_project_publications SET spec_json='{}'`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, action := pageActionBoundaryRig(t)
			action.Publication = 1
			if err := CheckDeclaredPageAction(t.Context(), db, "user", "ws", action); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.sql, tc.args...); err != nil {
				t.Fatal(err)
			}
			if err := CheckDeclaredPageAction(t.Context(), db, "user", "ws", action); !errors.Is(err, ErrInvocationAuthorityRevoked) {
				t.Fatalf("changed authority accepted: %v", err)
			}
		})
	}
}

func TestPageActionAuthorityRejectsMalformedOrIncompleteDurableIdentity(t *testing.T) {
	db, action := pageActionBoundaryRig(t)
	for _, raw := range []string{"not-json", `{}`, strings.TrimPrefix(action.Authority(), pageActionAuthorityPrefix) + ` {}`, strings.Replace(strings.TrimPrefix(action.Authority(), pageActionAuthorityPrefix), `"page_id":"page"`, `"page_id":"page","unknown":true`, 1)} {
		in := RunInput{WorkspaceID: "ws", InvokingUserID: "user", InvocationAuthority: pageActionAuthorityPrefix + raw}
		if err := checkPageActionAuthority(t.Context(), db, in, "OWNER"); !errors.Is(err, ErrInvocationAuthorityRevoked) {
			t.Fatalf("malformed identity accepted: %v", err)
		}
	}
	for _, change := range []func(*PageActionInvocation){
		func(a *PageActionInvocation) { a.PageID = "" }, func(a *PageActionInvocation) { a.PanelID = "" }, func(a *PageActionInvocation) { a.ActionID = "" }, func(a *PageActionInvocation) { a.PipelineID = "" }, func(a *PageActionInvocation) { a.ActionDigest = "" }, func(a *PageActionInvocation) { a.Publication = -1 },
	} {
		bad := action
		change(&bad)
		if err := CheckDeclaredPageAction(t.Context(), db, "user", "ws", bad); !errors.Is(err, ErrInvocationAuthorityRevoked) {
			t.Fatalf("incomplete authority accepted: %v", err)
		}
	}
	for _, who := range [][2]string{{"", "ws"}, {"user", ""}, {"missing", "ws"}, {"user", "other"}} {
		if err := CheckDeclaredPageAction(t.Context(), db, who[0], who[1], action); !errors.Is(err, ErrInvocationAuthorityRevoked) {
			t.Fatalf("missing actor accepted: %v", err)
		}
	}
	if err := CheckDeclaredPageAction(t.Context(), nil, "user", "ws", action); !errors.Is(err, ErrInvocationAuthorityRevoked) {
		t.Fatalf("missing storage accepted: %v", err)
	}
}

func TestPageActionStorageAndMalformedSpecFailClosed(t *testing.T) {
	for _, tc := range []struct{ name, sql, part string }{
		{"query failure", `DROP TABLE page_panels`, "authority lookup"},
		{"invalid document", `UPDATE pages SET spec_json='{'`, "authority spec"},
		{"publication query", `DROP TABLE page_project_live`, "publication authority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, action := pageActionBoundaryRig(t)
			action.Publication = 1
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			err := CheckDeclaredPageAction(t.Context(), db, "user", "ws", action)
			if err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("lost storage failure: %v", err)
			}
		})
	}
}

func TestPageActionDigestRejectsUnserializableContract(t *testing.T) {
	action := &pages.PanelAction{Params: map[string]any{"value": math.NaN()}}
	if got := PageActionDigest(action); got != "" {
		t.Fatalf("invalid contract got digest %q", got)
	}
}
