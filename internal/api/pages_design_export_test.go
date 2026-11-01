package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (f *designAuthorityFixture) export(t *testing.T, user, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := f.request(t, user, http.MethodGet, "/export"+query, "")
	w := httptest.NewRecorder()
	f.h.ExportDesign(w, r)
	return w
}

// v2 is shared and carries a prototype link plus hostile layer content; v3 is
// a later private version and the live document has unsaved private changes.
func newDesignExportFixture(t *testing.T) *designAuthorityFixture {
	t.Helper()
	f := newDesignAuthorityFixture(t)
	ops := `{"operations":[
		{"op":"upsert","node":{"id":"cart","type":"frame","name":"<script>alert(1)</script>","x":0,"y":0,"width":360,"height":640,"fill":"#ffffff"}},
		{"op":"upsert","node":{"id":"pay","type":"frame","name":"Pay","x":400,"y":0,"width":360,"height":640,"fill":"#ffffff"}},
		{"op":"upsert","node":{"id":"go","type":"rect","parentId":"cart","name":"Checkout","x":24,"y":24,"width":120,"height":48,"fill":"#2563eb","linkTo":"pay"}},
		{"op":"upsert","node":{"id":"title","type":"text","parentId":"cart","name":"Title","x":24,"y":100,"width":300,"height":30,"text":"SHARED \"><img src=x onerror=alert(2)>","fill":"red;background:url(https://evil.test)"}}
	]}`
	if w := f.call(t, f.owner, "apply", ops); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.call(t, f.owner, "share", `{"version":2}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"PRIVATE_SAVED"}}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	f.liveOnly(t, `[{"op":"upsert","node":{"id":"title","text":"PRIVATE_LIVE"}}]`)
	return f
}

func exportVersion(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("export %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("content type %q", got)
	}
	if got := w.Header().Get("Content-Security-Policy"); got != "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox" {
		t.Errorf("CSP header %q", got)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Error("export lacks nosniff/no-store")
	}
	version := w.Header().Get("X-Crewship-Design-Version")
	if want := `attachment; filename="board-design-v` + version + `.html"`; w.Header().Get("Content-Disposition") != want {
		t.Errorf("disposition %q, want %q", w.Header().Get("Content-Disposition"), want)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("Design version "+version+" ")) {
		t.Error("file header does not name the exported version")
	}
	return version
}

func TestDesignExportAuthorization(t *testing.T) {
	f := newDesignExportFixture(t)
	for _, user := range []string{"export-editor", "export-reader", "export-commenter", "export-member", "export-outsider"} {
		mustExec(t, f.h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, user, user+"@example.test", user)
		if user != "export-outsider" {
			mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.workspace, user)
		}
	}
	for user, level := range map[string]string{"export-editor": "write", "export-reader": "read", "export-commenter": "comment"} {
		if w := f.grant(t, user, level); w.Code != 200 {
			t.Fatal(user, w.Code, w.Body)
		}
	}
	type want struct {
		status        int
		version, text string
	}
	for _, tc := range []struct {
		user, query string
		want        want
	}{
		{f.owner, "", want{200, "3", "PRIVATE_SAVED"}},
		{f.owner, "?format=html&version=2", want{200, "2", "SHARED"}},
		{f.owner, "?version=1", want{200, "1", ""}},
		{f.owner, "?version=99", want{404, "", ""}},
		{"export-editor", "", want{200, "3", "PRIVATE_SAVED"}},
		{"export-editor", "?version=2", want{200, "2", "SHARED"}},
		{"export-reader", "", want{200, "2", "SHARED"}},
		{"export-reader", "?version=2", want{200, "2", "SHARED"}},
		{"export-reader", "?version=3", want{404, "", ""}},
		{"export-reader", "?version=1", want{404, "", ""}},
		{"export-reader", "?version=99", want{404, "", ""}},
		{"export-commenter", "", want{200, "2", "SHARED"}},
		{"export-commenter", "?version=3", want{404, "", ""}},
	} {
		t.Run(tc.user+tc.query, func(t *testing.T) {
			w := f.export(t, tc.user, tc.query)
			if w.Code != tc.want.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want.status, w.Body)
			}
			if tc.want.status != 200 {
				if bytes.Contains(w.Body.Bytes(), []byte("PRIVATE")) || bytes.Contains(w.Body.Bytes(), []byte("SHARED")) {
					t.Fatal("refusal leaked content")
				}
				return
			}
			if got := exportVersion(t, w); got != tc.want.version {
				t.Fatalf("exported v%s, want v%s", got, tc.want.version)
			}
			if tc.want.text != "" && !bytes.Contains(w.Body.Bytes(), []byte(tc.want.text)) {
				t.Errorf("export lacks %q", tc.want.text)
			}
			if bytes.Contains(w.Body.Bytes(), []byte("PRIVATE_LIVE")) {
				t.Error("export read the unsaved live document")
			}
			if tc.want.version == "2" && bytes.Contains(w.Body.Bytes(), []byte("PRIVATE_SAVED")) {
				t.Error("shared export contains a later version")
			}
		})
	}
	// Refusals answer exactly like the canonical Design read, so an export
	// probe learns nothing GET .../design would not already say.
	mustExec(t, f.h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, f.page)
	for _, user := range []string{"export-member", "export-outsider"} {
		for _, query := range []string{"", "?version=2", "?version=3", "?version=99"} {
			get := f.request(t, user, http.MethodGet, query, "")
			gw := httptest.NewRecorder()
			f.h.GetDesign(gw, get)
			ew := f.export(t, user, query)
			if ew.Code == 200 || ew.Code != gw.Code || ew.Body.String() != gw.Body.String() {
				t.Errorf("%s%s: export %d %s, read %d %s", user, query, ew.Code, ew.Body, gw.Code, gw.Body)
			}
		}
	}
	// Request validation runs only after admission: a malformed export of an
	// invisible Design still answers like a missing one.
	for _, query := range []string{"?format=pdf", "?version=abc", "?live=1"} {
		if w := f.export(t, "export-member", query); w.Code != 404 {
			t.Errorf("invisible Design with %s answered %d %s", query, w.Code, w.Body)
		}
	}
	// A member of another workspace cannot name this Page from there.
	other := "cdesignexportotherworkspace"
	mustExec(t, f.h.db, `INSERT INTO workspaces(id,name,slug) VALUES(?,?,?)`, other, "Other", "design-export-other")
	mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'OWNER')`, generateCUID(), other, "export-outsider")
	r := f.request(t, "export-outsider", http.MethodGet, "/export?version=2", "")
	r = r.WithContext(withWorkspace(r.Context(), other, "OWNER"))
	w := httptest.NewRecorder()
	f.h.ExportDesign(w, r)
	if w.Code != 404 {
		t.Errorf("foreign workspace export %d %s", w.Code, w.Body)
	}
}

func TestDesignExportContentIsInertAndDeterministic(t *testing.T) {
	f := newDesignExportFixture(t)
	first := f.export(t, f.owner, "?version=2")
	exportVersion(t, first)
	page := first.Body.String()
	for _, forbidden := range []string{"<script", "<img src=x", "url(", "https://", "http://"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("export contains %q", forbidden)
		}
	}
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "SHARED &#34;&gt;&lt;img src=x onerror=alert(2)&gt;", `<a class="layer rect hotspot" data-node-id="go" href="#screen-2" aria-label="Checkout: open Pay"`, `<section class="screen" id="screen-2" data-node-id="pay"`} {
		if !strings.Contains(page, want) {
			t.Errorf("export lacks %q", want)
		}
	}
	// Later edits, versions and live changes never alter an exported version.
	if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"LATER"}}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	f.liveOnly(t, `[{"op":"delete","id":"go"}]`)
	again := f.export(t, f.owner, "?version=2")
	if !bytes.Equal(first.Body.Bytes(), again.Body.Bytes()) {
		t.Fatal("the same version exported to different bytes")
	}
}

func TestDesignExportRefusals(t *testing.T) {
	f := newDesignExportFixture(t)
	for query, status := range map[string]int{"?version=0": 400, "?version=abc": 400, "?version=02": 400, "?format=pdf": 400, "?format=pptx": 400, "?version=2&version=3": 400, "?live=1": 400} {
		if w := f.export(t, f.owner, query); w.Code != status {
			t.Errorf("%s: %d %s", query, w.Code, w.Body)
		}
	}
	image := `{"operations":[{"op":"upsert","node":{"id":"photo","type":"image","parentId":"cart","x":0,"y":0,"width":360,"height":200,"src":"data:image/png;base64,` + strings.Repeat("A", 64<<10) + `"}}]}`
	if w := f.call(t, f.owner, "apply", image); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	previous := designExportMaxBytes
	designExportMaxBytes = 32 << 10
	t.Cleanup(func() { designExportMaxBytes = previous })
	w := f.export(t, f.owner, "")
	var body struct{ Error, Code string }
	if w.Code != 413 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Code != "design_export_limit" || !strings.Contains(body.Error, "file limit") {
		t.Fatalf("over-limit export: %d %s", w.Code, w.Body)
	}
	designExportMaxBytes = previous
	if w := f.export(t, f.owner, ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`<img src="data:image/png;base64,AAAA`)) {
		t.Fatalf("stored image not embedded: %d", w.Code)
	}
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "false")
	if w := f.export(t, f.owner, ""); w.Code != 404 {
		t.Fatalf("export with Design off: %d %s", w.Code, w.Body)
	}
}

func TestDesignExportByWorkspaceAdminIsAudited(t *testing.T) {
	f := newDesignExportFixture(t)
	user := "design-export-admin"
	f.addUser(t, user)
	mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
	mustExec(t, f.h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, f.page)
	if got := exportVersion(t, f.export(t, user, "?version=3")); got != "3" {
		t.Fatalf("admin exported v%s", got)
	}
	var count int
	if err := f.h.db.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='design.workspace_admin_accessed' AND user_id=? AND entity_id=? AND json_extract(metadata,'$.operation')='export'`, user, f.page).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("admin export audit rows %d", count)
	}
}
