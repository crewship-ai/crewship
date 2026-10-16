package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"

	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesImagesActualRouterUpgradeRestoreCommentsAndCascade(t *testing.T) {
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	f.r.pages.artifactStoragePath = storageDir(t)
	const page = "image-slide-page"
	wmSeedPage(t, f.db, "ws-old", page, page, "Image Slides", "boss", "")
	base := "/api/v1/pages/" + page + "/slides"
	uploaded := f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/assets?mutation_id=image", string(pageAssetPNG(t)))
	wantCode(t, uploaded, 201, "real asset upload")
	asset := assetMeta(t, uploaded)
	deck := slideTestDeck(t, page)
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 1, MutationID: "init", Deck: deck})), 200, "initialize v1")
	read := func() slides.State {
		t.Helper()
		response := f.slideDo(f.boss, "GET", base, "")
		wantCode(t, response, 200, "read")
		var state slides.State
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	state := read()
	slideID := state.Deck.Slides[0].ID
	version := 2
	element := slides.Element{ID: "image_element", Type: "image", X: 0, Y: 0, Width: 100, Height: 100, Image: &slides.Image{AssetID: asset.ID, ExpectedSHA256: asset.SHA256, Alt: "Local PNG"}}
	hash, _ := slides.SlideHash(state.Deck.Slides[0])
	request := slides.PatchRequest{PageID: page, SchemaVersion: 1, BaseRev: 1, MutationID: "attach", TargetSchemaVersion: &version, IfContentHash: state.ContentHash, Operations: []slides.Operation{{Op: "insert_element", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Element: &element}}}
	// Force a fault AFTER promotion in the production hook: every durable write
	// including the head/revision and asset state must roll back together.
	mustExec(t, f.db, `CREATE TRIGGER image_test_pin_failure BEFORE INSERT ON slides_revision_assets BEGIN SELECT RAISE(ABORT,'synthetic pin failure'); END`)
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, request)), 500, "pin failure")
	var retained string
	if err := f.db.QueryRow(`SELECT state FROM page_assets WHERE id=?`, asset.ID).Scan(&retained); err != nil || retained != "staged" {
		t.Fatalf("promotion rollback %s %v", retained, err)
	}
	if got := read(); got.Revision != 1 || got.SchemaVersion != 1 {
		t.Fatal("failed pin changed head")
	}
	mustExec(t, f.db, `DROP TRIGGER image_test_pin_failure`)
	element.Image.ExpectedSHA256 = strings.Repeat("b", 64)
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, request)), 400, "wrong SHA")
	element.Image.ExpectedSHA256 = asset.SHA256
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, request)), 200, "atomic first image")
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, request)), 200, "exact replay")
	state = read()
	if state.SchemaVersion != 2 || state.Revision != 2 {
		t.Fatal(state)
	}
	var pins int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=?`, page).Scan(&pins); err != nil || pins != 1 {
		t.Fatalf("pin count %d %v", pins, err)
	}
	if _, err := f.db.Exec(`DELETE FROM slides_revision_assets WHERE page_id=?`, page); err == nil {
		t.Fatal("standalone pin deletion accepted")
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=?`, page).Scan(&pins); err != nil || pins != 1 {
		t.Fatal("pin delete did not roll back", err)
	}
	create := slides.CreateCommentRequest{PageID: page, SchemaVersion: 1, BaseRev: 2, MutationID: "image_thread", Anchor: slides.CommentAnchor{AnchorSchemaVersion: 1, Kind: "element", SlideID: slideID, ElementID: element.ID, Revision: 2}, Body: "Image comment"}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/comments", slideJSON(t, create)), 200, "protocol1 image comment on deck2")
	imageHash, _ := slides.ElementHash(element)
	remove := slides.PatchRequest{PageID: page, SchemaVersion: 2, BaseRev: 2, MutationID: "delete_image", Operations: []slides.Operation{{Op: "delete_element", SlideID: slideID, ElementID: element.ID, IfHash: imageHash}}}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, remove)), 200, "delete image")
	var status string
	if err := f.db.QueryRow(`SELECT status FROM slides_comment_threads WHERE id='image_thread'`).Scan(&status); err != nil || status != "detached" {
		t.Fatalf("comment detach %s %v", status, err)
	}
	wantCode(t, f.slideDo(f.boss, "GET", "/api/v1/pages/"+page+"/assets/"+asset.ID+"/bytes", ""), 200, "historic bytes remain")
	restore := slides.RestoreRequest{PageID: page, SchemaVersion: 2, BaseRev: 3, MutationID: "restore_image", IfOrderVersion: state.OrderVersion, Revision: 2}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/restore", slideJSON(t, restore)), 200, "restore image revision")
	if err := f.db.QueryRow(`SELECT status FROM slides_comment_threads WHERE id='image_thread'`).Scan(&status); err != nil || status != "restored" {
		t.Fatalf("comment restore %s %v", status, err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=?`, page).Scan(&pins); err != nil || pins != 2 {
		t.Fatalf("historic + restore pins %d %v", pins, err)
	}

	for _, mixed := range []slides.RestoreRequest{
		{PageID: page, SchemaVersion: 2, BaseRev: 4, MutationID: "source_v1", IfOrderVersion: state.OrderVersion, Revision: 1},
		{PageID: page, SchemaVersion: 1, BaseRev: 5, MutationID: "source_v2", IfOrderVersion: state.OrderVersion, Revision: 2},
	} {
		wantCode(t, f.slideDo(f.boss, "POST", base+"/restore", slideJSON(t, mixed)), 200, "mixed-version restore")
		got := read()
		wantSchema := 1
		wantStatus := "detached"
		if mixed.Revision == 2 {
			wantSchema = 2
			wantStatus = "restored"
		}
		if got.SchemaVersion != wantSchema {
			t.Fatal("restore silently changed source schema")
		}
		if err := f.db.QueryRow(`SELECT status FROM slides_comment_threads WHERE id='image_thread'`).Scan(&status); err != nil || status != wantStatus {
			t.Fatalf("mixed comment projection %s %v", status, err)
		}
	}
	create.SchemaVersion = 2
	create.MutationID = "unsupported_comment_protocol"
	create.BaseRev = 6
	wantCode(t, f.slideDo(f.boss, "POST", base+"/comments", slideJSON(t, create)), 400, "deck2 must not broaden comment protocol")
	// Full Page cascade must satisfy deferred composite asset FK and no-delete
	// guards regardless of SQLite cascade visitation order.
	wantCode(t, f.slideDo(f.boss, "DELETE", "/api/v1/pages/"+page, ""), 204, "full Page cascade")
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_revision_assets", "page_assets", "slides_comment_threads"} {
		var n int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE page_id=?`, page).Scan(&n); err != nil || n != 0 {
			t.Fatalf("cascade %s %d %v", table, n, err)
		}
	}
}

func TestSlidesImagesActualRouterExplicitSchema2Initialize(t *testing.T) {
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	f.r.pages.artifactStoragePath = storageDir(t)
	const page = "init-image-page"
	wmSeedPage(t, f.db, "ws-old", page, page, "Image initialize", "boss", "")
	uploaded := f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/assets?mutation_id=first", string(pageAssetPNG(t)))
	wantCode(t, uploaded, 201, "upload")
	asset := assetMeta(t, uploaded)
	deck := slideTestDeck(t, page)
	deck.SchemaVersion = 2
	deck.Slides[0].Elements = append(deck.Slides[0].Elements, slides.Element{ID: "init_image", Type: "image", Width: 10, Height: 10, Image: &slides.Image{AssetID: asset.ID, ExpectedSHA256: asset.SHA256, Alt: "Initial image"}})
	wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 2, MutationID: "init_v2", Deck: deck})), 200, "explicit schema2 initialize")
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=? AND rev=1`, page).Scan(&n); err != nil || n != 1 {
		t.Fatalf("initialize pins %d %v", n, err)
	}
}

func TestSlidesImagesEncryptedBackupForkRetainsNonHeadRefs(t *testing.T) {
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	f.r.pages.artifactStoragePath = storageDir(t)
	const page = "backup-image-page"
	wmSeedPage(t, f.db, "ws-old", page, page, "Backup Image", "boss", "")
	uploaded := f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/assets?mutation_id=first", string(pageAssetPNG(t)))
	wantCode(t, uploaded, 201, "upload")
	asset := assetMeta(t, uploaded)
	deck := slideTestDeck(t, page)
	deck.SchemaVersion = 2
	image := slides.Element{ID: "retained_image", Type: "image", Width: 10, Height: 10, Image: &slides.Image{AssetID: asset.ID, ExpectedSHA256: asset.SHA256, Alt: "History image"}}
	deck.Slides[0].Elements = append(deck.Slides[0].Elements, image)
	base := "/api/v1/pages/" + page + "/slides"
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 2, MutationID: "init", Deck: deck})), 200, "initialize")
	imageHash, _ := slides.ElementHash(image)
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, slides.PatchRequest{PageID: page, SchemaVersion: 2, BaseRev: 1, MutationID: "delete", Operations: []slides.Operation{{Op: "delete_element", SlideID: deck.Slides[0].ID, ElementID: image.ID, IfHash: imageHash}}})), 200, "remove only head image")
	const pass = "slides-image-backup-qualified-password"
	made, err := backup.CreateBackup(t.Context(), f.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: "ws-old", OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: f.r.pages.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}

	for _, broken := range []string{"missing", "corrupt", "metadata"} {
		t.Run(broken+"-bundle", func(t *testing.T) {
			sourcePath, err := attachmentBlobPath(f.r.pages.artifactStoragePath, "ws-old", asset.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(sourcePath, original, 0600); err != nil {
					t.Error(err)
				}
			})
			if broken == "metadata" {
				mustExec(t, f.db, `DROP TRIGGER page_asset_immutable`)
				mustExec(t, f.db, `UPDATE page_assets SET width=width+1 WHERE id=?`, asset.ID)
				t.Cleanup(func() { mustExec(t, f.db, `UPDATE page_assets SET width=width-1 WHERE id=?`, asset.ID) })
			} else if broken == "missing" {
				err = os.Remove(sourcePath)
			} else {
				err = os.WriteFile(sourcePath, []byte("corrupt image bytes"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			damaged, err := backup.CreateBackup(t.Context(), f.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: "ws-old", OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: f.r.pages.artifactStoragePath})
			if err != nil {
				t.Fatal(err)
			}
			target := setupTestDB(t)
			_, err = backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: damaged.Path, Passphrase: pass, Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: filepath.Join(t.TempDir(), "storage")})
			if err == nil {
				t.Fatal("broken encrypted image bundle restored successfully")
			}
			var rows int
			if err := target.QueryRow(`SELECT COUNT(*) FROM slides_heads`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("broken bundle committed image rows %d %v", rows, err)
			}
		})
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := setupTestDB(t)
			root := storageDir(t)
			options := backup.RestoreOptions{Path: made.Path, Passphrase: pass, Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: root}
			if fork {
				options.AsWorkspace = "image-fork"
			}
			result, err := backup.RestoreBackup(t.Context(), target, options)
			if err != nil {
				t.Fatal(err)
			}
			var workspace, newPage, owner string
			if err = target.QueryRow(`SELECT h.workspace_id,h.page_id,p.owner_user_id FROM slides_heads h JOIN pages p ON p.id=h.page_id`).Scan(&workspace, &newPage, &owner); err != nil {
				t.Fatal(err)
			}
			if fork && (newPage == page || workspace == "ws-old") {
				t.Fatal("fork did not namespace images")
			}
			var canonical []byte
			var savedhash string
			if err = target.QueryRow(`SELECT canonical_json,content_hash FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=1`, workspace, newPage).Scan(&canonical, &savedhash); err != nil {
				t.Fatal(err)
			}
			restored, err := slides.Decode(canonical)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := slides.DeckHash(restored)
			if err != nil || hash != savedhash || restored.DeckID != newPage {
				t.Fatal("fork canonical hash/Page mismatch", err)
			}
			refs := slides.ImageReferences(restored)
			if len(refs) != 1 {
				t.Fatal("lost non-head image")
			}
			var newAsset string
			for id, sha := range refs {
				newAsset = id
				if sha != asset.SHA256 || (fork && id == asset.ID) {
					t.Fatal("wrong image mapping")
				}
			}
			var count int
			if err = target.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=? AND rev=1 AND asset_id=? AND expected_sha256=?`, newPage, newAsset, asset.SHA256).Scan(&count); err != nil || count != 1 {
				t.Fatal("fork exact pin missing", err)
			}
			bytes, err := readPageAssetBlob(root, workspace, asset.SHA256)
			if err != nil || attachmentDigest(bytes) != asset.SHA256 {
				t.Fatal("fork image bytes missing", err)
			}
			router, err := NewRouter(target, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithStoragePath(root))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			credential := mintTokenFor(t, target, owner, "restored-image-session")
			restoredFixture := &instanceFixture{t: t, db: target, r: router}
			response := restoredFixture.do(credential, "GET", "/api/v1/pages/"+page+"/assets/"+newAsset+"/bytes?workspace_id="+result.RestoredWorkspaceID, "")
			wantCode(t, response, 200, "authorized restored bytes")
			if attachmentDigest(response.Body.Bytes()) != asset.SHA256 {
				t.Fatal("restored route served wrong bytes")
			}
		})
	}
}
