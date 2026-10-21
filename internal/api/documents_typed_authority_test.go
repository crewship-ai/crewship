package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentAuthorityRejectsFutureStoredSchemaWithoutContent(t *testing.T) {
	for _, operation := range []string{"state", "initialize"} {
		t.Run(operation, func(t *testing.T) {
			rig := pagesTransferSetup(t)
			const page = "typed-authority-page"
			rig.seedPage(t, page, page, "Document", rig.adminID, "")
			store := documents.New(rig.db, documents.Options{})
			key := documents.Key{WorkspaceID: rig.wsID, PageID: page}
			allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + rig.adminID, nil }
			snapshot := []byte("retained-private-content")
			projection := []byte(`[{"id":"private-block","hash":"private-hash"}]`)
			if _, err := store.InitRequest(t.Context(), key, "original-create", "original-fingerprint", documents.LatestSchema, snapshot, projection, allow); err != nil {
				t.Fatal(err)
			}
			h := &documentAuthority{pages: NewPageHandler(rig.db, nil, newTestLogger()), store: store}
			body := fmt.Sprintf(`{"id":"original-create","fingerprint":"original-fingerprint","schema":%d,"snapshot":%q,"blocks":%s}`, documents.LatestSchema, base64.StdEncoding.EncodeToString(snapshot), projection)
			call := func() *httptest.ResponseRecorder {
				request := ownershipRequest(t, rig, "POST", "/", body)
				request.SetPathValue("page", page)
				request.SetPathValue("operation", operation)
				out := httptest.NewRecorder()
				h.operation(out, request)
				return out
			}
			if control := call(); control.Code != 200 {
				t.Fatalf("control=%d %s", control.Code, control.Body)
			}
			if _, err := rig.db.Exec("UPDATE documents SET schema_version=99 WHERE workspace_id=? AND page_id=?", key.WorkspaceID, key.PageID); err != nil {
				t.Fatal(err)
			}
			out := call()
			if out.Code != 409 {
				t.Fatalf("future schema=%d want409 body=%s", out.Code, out.Body)
			}
			for _, secret := range []string{base64.StdEncoding.EncodeToString(snapshot), "private-block", "private-hash"} {
				if strings.Contains(out.Body.String(), secret) {
					t.Fatalf("unsupported schema disclosed private document content")
				}
			}
		})
	}
}
