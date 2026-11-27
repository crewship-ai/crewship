package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// "Open as a Sheet": the uploaded xlsx or xlsm becomes version 1 of a new workbook Sheet, byte for
// byte, under initialize's ownership, storage admission and lease; nothing is converted.
func TestWorkbookSheetInitializesFromTheUploadedFile(t *testing.T) {
	f, h, ctx := craftNativeHTTPFixture(t)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	call := func(page string, body []byte, contentType, fileName string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/initialize", bytes.NewReader(body)).WithContext(ctx)
		req.SetPathValue("slug", "@"+page)
		req.Header.Set("Content-Type", contentType)
		if fileName != "" {
			req.Header.Set("X-Craft-File-Name", fileName)
		}
		w := httptest.NewRecorder()
		d.initialize(w, req)
		return w
	}
	upload := craftTestWorkbook(t, "xlsm", "uploaded")
	f.seedPage(t, "wb-new", "wb-new", "Rozpočet", f.userID, "")
	w := call("wb-new", upload, mooring.WorkbookMediaTypeXLSM, "Rozpo%C4%8Det%202026.xlsm")
	if w.Code != 201 {
		t.Fatalf("initialize %d %s", w.Code, w.Body)
	}
	var got craftNativeInitializeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Version != "1" || got.Format != "xlsm" || got.ContentHash != workbookHash(upload) || got.ApplicationID != "gridcraft" || got.Token == "" {
		t.Fatalf("response %+v %v", got, err)
	}
	v := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	m, err := v.ReadVersion(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "wb-new"}, 1, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil })
	if err != nil || m.EntryPoint != "workbook.xlsm" || !bytes.Equal(m.Files["workbook.xlsm"].Content, upload) || m.Files["workbook.xlsm"].MediaType != mooring.WorkbookMediaTypeXLSM {
		t.Fatalf("version 1 %+v %v", m.EntryPoint, err)
	}
	for _, tc := range []struct {
		name, page, contentType, fileName string
		body                              []byte
		status                            int
		code                              string
	}{
		{"native JSON on a workbook server", "wb-json", "application/json", "", []byte(`{"application_id":"gridcraft","bytes":"e30="}`), 415, "unsupported"},
		{"declared xlsx, is xlsm", "wb-type", mooring.WorkbookMediaTypeXLSX, "", upload, 415, "unsupported"},
		{"not a workbook", "wb-not", mooring.WorkbookMediaTypeXLSX, "", []byte("PK\x03\x04 nothing here at all, not a zip"), 415, "not_workbook"},
		{"file name with a path", "wb-name", mooring.WorkbookMediaTypeXLSM, "..%2Fetc%2Fpasswd", upload, 400, ""},
	} {
		f.seedPage(t, tc.page, tc.page, tc.name, f.userID, "")
		w := call(tc.page, tc.body, tc.contentType, tc.fileName)
		if w.Code != tc.status || tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=?`, tc.page).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d versions written", tc.name, n)
		}
	}
}

// A Sheet opened from a file in a chat (X-Craft-Chat) belongs to that chat with its first version,
// in the same transaction; a chat that is not the caller's, or none, creates nothing.
func TestWorkbookInitializeAddsTheSheetToTheCallersChat(t *testing.T) {
	f, h, ctx := craftNativeHTTPFixture(t)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	mustExec(t, f.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('chat-agent',?,'Morgan','morgan')`, f.wsID)
	mustExec(t, f.db, `INSERT INTO chats(id,agent_id,workspace_id,created_by) VALUES('chat-own','chat-agent',?,?)`, f.wsID, f.userID)
	mustExec(t, f.db, `INSERT INTO chats(id,agent_id,workspace_id,created_by) VALUES('chat-other','chat-agent',?,NULL)`, f.wsID)
	call := func(page, chat string) *httptest.ResponseRecorder {
		f.seedPage(t, page, page, page, f.userID, "")
		req := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/initialize", bytes.NewReader(craftTestWorkbook(t, "xlsx", page))).WithContext(ctx)
		req.SetPathValue("slug", "@"+page)
		req.Header.Set("Content-Type", mooring.WorkbookMediaTypeXLSX)
		req.Header.Set("X-Craft-File-Name", page+".xlsx")
		req.Header.Set("X-Craft-Chat", chat)
		w := httptest.NewRecorder()
		d.initialize(w, req)
		return w
	}
	refs := func(page string) (n int) {
		if err := f.db.QueryRow(`SELECT count(*) FROM artifact_chat_refs WHERE page_id=?`, page).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	w := call("wb-chat", "chat-own")
	var got craftNativeInitializeResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.ChatRef || refs("wb-chat") != 1 {
		t.Fatalf("own chat %d %s", w.Code, w.Body)
	}
	if exists, removed := agentFileState(t, f.db, "chat-agent", "wb-chat"); !exists || removed {
		t.Fatalf("the Sheet is not kept with the chat's agent: %v %v", exists, removed)
	}
	for _, chat := range []string{"chat-other", "chat-missing", "bad id!"} {
		page := "wb-" + strings.NewReplacer(" ", "", "!", "").Replace(chat)
		w = call(page, chat)
		var n int
		_ = f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=?`, page).Scan(&n)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"chat_not_found"`) || n != 0 || refs(page) != 0 {
			t.Errorf("%s: %d %s, %d versions", chat, w.Code, w.Body, n)
		}
	}
}
