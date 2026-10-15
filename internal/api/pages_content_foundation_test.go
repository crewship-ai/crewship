package api

import (
	"context"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/pages"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func seedStorageBoardPin(t *testing.T, h *PageHandler, ws, user, source string, revision int) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('storage-board',?,'storage-board','Storage board',?,'{}')`, ws, user)
	execOrFatal(t, h.db, `INSERT INTO page_boards(page_id,seq,format) VALUES('storage-board',1,?)`, pages.BoardFormat)
	execOrFatal(t, h.db, `INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,author_user_id) VALUES('storage-placement','storage-board',?,?,0,0,300,200,1,?)`, source, revision, user)
}

func boardAPIElement(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../pages/testdata/board-excalidraw-0.18.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var elements []json.RawMessage
	if err = json.Unmarshal(raw, &elements); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(elements[2], &fields); err != nil {
		t.Fatal(err)
	}
	fields["boundElements"] = nil
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func boardJobAgentCall(t *testing.T, h *PageHandler, workspace, token, id, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("requestId", id)
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	handler(w, r.WithContext(ctx))
	return w
}
