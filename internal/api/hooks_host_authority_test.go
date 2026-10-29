package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A workspace owner is not an instance administrator. Exercise the real
// authentication/workspace/router chain without executing any host command.
func TestHostHookRequiresInstanceAdministrator(t *testing.T) {
	f := newInstanceFixture(t)
	token := mintTokenFor(t, f.db, "carol", "hookauthoritycarol0000000000")
	path := "/api/v1/hooks?workspace_id=ws-new"
	control := f.do(token, http.MethodPost, path,
		`{"event":"post_tool_call","handler_kind":"http","handler_config":{"url":"https://example.test/hook"},"enabled":false}`)
	wantCode(t, control, http.StatusCreated, "ordinary workspace owner HTTP hook")
	result := f.do(token, http.MethodPost, path,
		`{"event":"post_tool_call","handler_kind":"shell","handler_config":{"command":"true"},"enabled":false}`)
	wantCode(t, result, http.StatusForbidden, "ordinary workspace owner host hook")
}

func TestHostHookAuthorityAcrossMutations(t *testing.T) {
	f := newInstanceFixture(t)
	token := mintTokenFor(t, f.db, "carol", "hookmutationcarol000000000")
	if _, err := f.db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('boss-hook-member','ws-new','boss','MEMBER')`); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/hooks"
	result := f.do(f.boss, http.MethodPost, base+"?workspace_id=ws-new", `{"event":"post_tool_call","handler_kind":"shell","handler_config":{"command":"true"},"enabled":false}`)
	wantCode(t, result, http.StatusCreated, "instance administrator with MEMBER workspace role")
	var row struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &row); err != nil || row.ID == "" {
		t.Fatalf("create response: %s %v", result.Body.String(), err)
	}
	for _, req := range []struct{ method, suffix, body string }{
		{http.MethodPatch, "", `{"handler_config":{"command":"changed"}}`},
		{http.MethodPatch, "", `{"handler_kind":"http","handler_config":{"url":"https://example.test"}}`},
		{http.MethodPost, "/enable", `{}`},
	} {
		wantCode(t, f.do(token, req.method, base+"/"+row.ID+req.suffix+"?workspace_id=ws-new", req.body), http.StatusForbidden, "workspace owner mutation of host hook")
	}
	// Unrestricted legacy tokens and explicitly scoped tokens have separate gates.
	if _, err := f.db.Exec(`UPDATE cli_tokens SET scopes='["workspace:admin"]' WHERE user_id='boss'`); err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.do(f.boss, http.MethodPost, base+"/"+row.ID+"/enable?workspace_id=ws-new", `{}`), http.StatusForbidden, "instance administrator without instance:admin scope")
	if _, err := f.db.Exec(`UPDATE cli_tokens SET scopes='["instance:admin","workspace:admin"]' WHERE user_id='boss'`); err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.do(f.boss, http.MethodPost, base+"/"+row.ID+"/enable?workspace_id=ws-new", `{}`), http.StatusOK, "explicit administrator approval")
	wantCode(t, f.do(token, http.MethodPost, base+"/"+row.ID+"/disable?workspace_id=ws-new", `{}`), http.StatusOK, "workspace owner can stop host execution")
	wantCode(t, f.do(token, http.MethodPost, base+"/"+row.ID+"/enable?workspace_id=ws-new", `{}`), http.StatusForbidden, "stopping execution does not grant re-enable authority")
	wantCode(t, f.do(f.boss, http.MethodPatch, base+"/"+row.ID+"?workspace_id=ws-old", `{"enabled":true}`), http.StatusNotFound, "workspace isolation")
}

func TestHostHookInlineRoutesRetainNonHostMemberDenial(t *testing.T) {
	f := newInstanceFixture(t)
	token := seedRoleMemberToken(t, f.db, "ws-old", "hook-member", "MEMBER", "hookmember00000000000000000")
	created := f.do(f.boss, http.MethodPost, "/api/v1/hooks?workspace_id=ws-old", `{"event":"post_tool_call","handler_kind":"http","handler_config":{"url":"https://example.test"},"enabled":false}`)
	wantCode(t, created, http.StatusCreated, "HTTP control")
	var row struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil || row.ID == "" {
		t.Fatal(created.Body.String())
	}
	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/hooks", `{"event":"post_tool_call","handler_kind":"http","handler_config":{"url":"https://example.test"}}`},
		{http.MethodPatch, "/api/v1/hooks/" + row.ID, `{"enabled":true}`},
		{http.MethodPost, "/api/v1/hooks/" + row.ID + "/enable", `{}`},
		{http.MethodPost, "/api/v1/hooks/" + row.ID + "/disable", `{}`},
	} {
		wantCode(t, f.do(token, req.method, req.path+"?workspace_id=ws-old", req.body), http.StatusForbidden, "ordinary MEMBER non-host mutation")
	}
}
