package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentLimitsWireProductionOptionsAndTemporaryError(t *testing.T) {
	r := &Router{}
	WithDocumentLimits(config.DocumentsConfig{MaxDocumentsPerWorkspace: 256, MaxOperationsPerWindow: 2000, OperationWindowSeconds: 600})(r)
	if r.documentStoreOptions.MaxDocuments != 256 || r.documentStoreOptions.MaxCommits != 2000 || r.documentStoreOptions.ReceiptWindow != 10*time.Minute {
		t.Fatalf("production limits not wired: %+v", r.documentStoreOptions)
	}
	for _, tc := range []struct {
		err  error
		code string
	}{{documents.ErrRateLimit, "document_rate_limit"}, {documents.ErrQuota, "document_quota"}} {
		w := httptest.NewRecorder()
		documentAuthorityError(w, tc.err)
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 429 || body["code"] != tc.code {
			t.Fatalf("incorrect admission classification: %d %v", w.Code, body)
		}
	}
}

func TestDocumentConfiguredCapacityReachesRouterAuthorityStore(t *testing.T) {
	h, workspace, user, page := documentAuthorityFixture(t)
	r, err := NewRouter(h.db, "synthetic-document-limits-router-secret", newTestLogger(), WithDocumentLimits(config.DocumentsConfig{MaxDocumentsPerWorkspace: 1, MaxOperationsPerWindow: 1}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Shutdown)
	handler, err := r.DocumentAuthority(t.Context(), "synthetic-document-limits-service-secret")
	if err != nil {
		t.Fatal(err)
	}
	store := handler.(*documentAuthority).store
	allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + user, nil }
	key := documents.Key{WorkspaceID: workspace, PageID: page}
	if _, err = store.Init(t.Context(), key, 1, []byte("baseline"), []byte(`[{"id":"one","hash":"h1"}]`), allow); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(t.Context(), key, "node", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Checkpoint(t.Context(), key, lease, "cp", []byte("baseline"), 0, allow); !errors.Is(err, documents.ErrRateLimit) {
		t.Fatalf("configured operation limit not used by actual authority: %v", err)
	}
	// Capacity is checked before insertion; the nonexistent second Page would
	// otherwise fail its FK/owner invariant instead of the configured quota.
	if _, err = store.Init(t.Context(), documents.Key{WorkspaceID: workspace, PageID: "not-created"}, 1, []byte("baseline"), []byte(`[]`), allow); !errors.Is(err, documents.ErrQuota) {
		t.Fatalf("configured workspace capacity not used: %v", err)
	}
}
