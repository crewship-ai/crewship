package api

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesSelectedSchema2ImageEncryptedForkBinding(t *testing.T) {
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	f.r.pages.artifactStoragePath = storageDir(t)
	const page = "selected-image-page"
	wmSeedPage(t, f.db, "ws-old", page, page, "Selected image", "boss", "")
	uploaded := f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/assets?mutation_id=image", string(pageAssetPNG(t)))
	wantCode(t, uploaded, 201, "actual PNG upload")
	asset := assetMeta(t, uploaded)
	deck := slideTestDeck(t, page)
	deck.SchemaVersion = 2
	deck.Slides[0].Elements = append(deck.Slides[0].Elements, slides.Element{ID: "selected_image", Type: "image", Width: 100, Height: 100, Image: &slides.Image{AssetID: asset.ID, ExpectedSHA256: asset.SHA256, Alt: "Retained PNG"}})
	wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 2, MutationID: "initialize", Deck: deck})), 200, "schema2 native initialize")
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('selected-crew','ws-old','Selected','selected')`)
	seedAgentRow(t, f.db, "selected-agent", "ws-old", "selected-crew", "Selected agent", "selected", "AGENT")
	mustExec(t, f.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('selected-chat','ws-old','selected-agent','boss','private')`)
	key := slides.Key{WorkspaceID: "ws-old", PageID: page}
	store := f.r.pages.newSlidesStore()
	auth := func(context.Context, *sql.Tx) (string, error) { return "user:boss", nil }
	var selection slides.TextSelection
	for _, slide := range deck.Slides {
		for _, e := range slide.Elements {
			if e.Type == "text" && selection.ElementID == "" {
				hash, _ := slides.ElementHash(e)
				selection = slides.TextSelection{SelectionSchemaVersion: 1, Kind: "text_range", SlideID: slide.ID, ElementID: e.ID, ElementHash: hash, From: 0, To: len(utf16.Encode([]rune(string([]rune(e.Text.Value)[0])))), Quote: string([]rune(e.Text.Value)[0])}
			}
		}
	}
	input := slides.SelectedRequestInput{PageID: page, TypeID: "slides", SchemaVersion: 1, BaseRev: 1, SubmissionID: "image-selected-submission", ChatID: "selected-chat", Instruction: "Insert selected text", Selection: selection}
	admission := slides.SelectedAdmission{HumanUserID: "boss", AgentID: "selected-agent", ChatID: "selected-chat", AudienceJSON: `{"trusted":"backup fixture"}`, CredentialKind: "session", CredentialID: "backup_session"}
	// Trusted Store fixture for admission; actual public admission/sidecar is
	// independently covered. Asset writes use the production pinning constructor.
	if _, err := store.AdmitSelectedRequest(t.Context(), key, "image-selected-request", input, admission, auth); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimSelectedRequest(t.Context(), key, "image-selected-request", auth); err != nil {
		t.Fatal(err)
	}
	if err := store.BindSelectedRun(t.Context(), key, "image-selected-request", "image-selected-run", auth, func(context.Context, *sql.Tx, slides.SelectedAuthorityState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	agentAuth := func(context.Context, *sql.Tx) (string, error) { return "agent:selected-agent:image-selected-run", nil }
	if _, err := store.ReplaceSelectedText(t.Context(), key, "image-selected-request", "image-selected-run", "Upravený český text\n🐎", agentAuth); err != nil {
		t.Fatal(err)
	}
	original, err := store.ReadSelectedRequest(t.Context(), key, "image-selected-request", auth)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := backup.CreateBackup(t.Context(), f.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: "ws-old", OutputDir: t.TempDir(), Passphrase: "selected-image-backup-passphrase", Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: f.r.pages.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	target := setupTestDB(t)
	root := filepath.Join(t.TempDir(), "storage")
	if _, err = backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: bundle.Path, Passphrase: "selected-image-backup-passphrase", AsWorkspace: "selected-image-fork", Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: root}); err != nil {
		t.Fatal(err)
	}
	var forkKey slides.Key
	var id string
	if err = target.QueryRow(`SELECT workspace_id,page_id,id FROM slides_requests`).Scan(&forkKey.WorkspaceID, &forkKey.PageID, &id); err != nil {
		t.Fatal(err)
	}
	forkStore := slides.New(target, slides.Options{})
	restored, err := forkStore.ReadSelectedRequest(t.Context(), forkKey, id, auth)
	if err != nil {
		t.Fatal(err)
	}
	r := restored.Request
	if !r.Restored || r.Status != "interrupted" || r.Version != 5 || r.BaseSchemaVersion != 2 || r.ResultRevision == nil || *r.ResultRevision != 2 || r.Selection != selection || r.BaseContentHash == original.Request.BaseContentHash || r.ResultHash == original.Request.ResultHash {
		t.Fatal("fork request bindings not remapped", r)
	}
	for _, rev := range []int64{1, 2} {
		state, err := forkStore.Read(t.Context(), forkKey, rev, auth)
		if err != nil {
			t.Fatal(err)
		}
		expected := r.BaseContentHash
		if rev == 2 {
			expected = r.ResultHash
		}
		if state.ContentHash != expected || state.Deck.SchemaVersion != 2 {
			t.Fatal("fork immutable revision/hash mismatch", state)
		}
		refs := slides.ImageReferences(state.Deck)
		if len(refs) != 1 {
			t.Fatal("image lost")
		}
		for imageID, sha := range refs {
			if imageID == asset.ID || sha != asset.SHA256 {
				t.Fatal("asset canonical remap mismatch")
			}
			var pins int
			if err = target.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE workspace_id=? AND page_id=? AND rev=? AND asset_id=? AND expected_sha256=?`, forkKey.WorkspaceID, forkKey.PageID, rev, imageID, sha).Scan(&pins); err != nil || pins != 1 {
				t.Fatal("actual retained image pin lost", pins, err)
			}
		}
	}
	blob, err := readPageAssetBlob(root, forkKey.WorkspaceID, asset.SHA256)
	if err != nil || attachmentDigest(blob) != asset.SHA256 {
		t.Fatal("PNG blob lost", err)
	}
}
