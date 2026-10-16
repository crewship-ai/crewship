package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentAuthorityReadsThroughSingleWriterConnection(t *testing.T) {
	rig := pagesTransferSetup(t)
	const page = "single-connection-document"
	rig.seedPage(t, page, page, "Document", rig.adminID, "")
	request := ownershipRequest(t, rig, "POST", "/", "")
	pages := NewPageHandler(rig.db, nil, newTestLogger())
	store := documents.New(rig.db, documents.Options{})
	key := documents.Key{WorkspaceID: rig.wsID, PageID: page}
	allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + rig.adminID, nil }
	if _, err := store.Init(t.Context(), key, 1, []byte("retained-document"), []byte(`[]`), allow); err != nil {
		t.Fatal(err)
	}
	rig.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	request = request.WithContext(ctx)
	handler := &documentAuthority{pages: pages, store: store}
	state, err := store.Read(ctx, key, handler.authorize(request, key, false, false))
	if err != nil || string(state.Snapshot) != "retained-document" {
		t.Fatalf("authorized document read requires a second connection: %v", err)
	}
}
