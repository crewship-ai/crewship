package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pageassets"
	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesImagesUniqueDecodedPixelBudgetWriteAndRestore(t *testing.T) {
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	f.r.pages.artifactStoragePath = storageDir(t)
	const page = "pixel-budget-page"
	wmSeedPage(t, f.db, "ws-old", page, page, "Pixel budget", "boss", "")
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, pageassets.MaxDimension, pageassets.MaxDimension))); err != nil {
		t.Fatal(err)
	}
	assets := []pageAssetMetadata{}
	for i := 0; i < 9; i++ {
		response := f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/assets?mutation_id=pixel_"+string(rune('a'+i)), raw.String())
		wantCode(t, response, 201, "maximum pixel raster upload")
		assets = append(assets, assetMeta(t, response))
	}
	deck := slideTestDeck(t, page)
	deck.SchemaVersion = 2
	imageElement := func(i int, id string) slides.Element {
		return slides.Element{ID: id, Type: "image", Width: 10, Height: 10, Image: &slides.Image{AssetID: assets[i].ID, ExpectedSHA256: assets[i].SHA256, Alt: "Pixel boundary"}}
	}
	for i := 0; i < 8; i++ {
		deck.Slides[0].Elements = append(deck.Slides[0].Elements, imageElement(i, "pixel_image_"+string(rune('a'+i))))
	}
	base := "/api/v1/pages/" + page + "/slides"
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 2, MutationID: "exact_boundary", Deck: deck})), 200, "eight 2048 squared unique assets meet 32Mi pixel limit")
	var state slides.State
	read := f.slideDo(f.boss, "GET", base, "")
	wantCode(t, read, 200, "read boundary")
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	var slide slides.Slide
	for _, item := range state.Deck.Slides {
		if item.ID == deck.Slides[0].ID {
			slide = item
		}
	}
	hash, _ := slides.SlideHash(slide)
	ninth := imageElement(8, "ninth_unique")
	request := slides.PatchRequest{PageID: page, SchemaVersion: 2, BaseRev: 1, MutationID: "excess_pixels", Operations: []slides.Operation{{Op: "insert_element", SlideID: slide.ID, IfHash: hash, Element: &ninth}}}
	denied := f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, request))
	wantCode(t, denied, 400, "ninth unique asset exceeds decoded pixels")
	if !strings.Contains(denied.Body.String(), "pixel") {
		t.Fatal("wrong budget refusal", denied.Body.String())
	}
	var head int64
	if err := f.db.QueryRow(`SELECT head_rev FROM slides_heads WHERE page_id=?`, page).Scan(&head); err != nil || head != 1 {
		t.Fatal("pixel refusal changed head", err)
	}
	var status string
	if err := f.db.QueryRow(`SELECT state FROM page_assets WHERE id=?`, assets[8].ID).Scan(&status); err != nil || status != "staged" {
		t.Fatal("pixel refusal retained ninth asset", err)
	}
	repeated := imageElement(0, "repeat_same_asset")
	request.MutationID = "repeated_reference"
	request.Operations[0].Element = &repeated
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, request)), 200, "repeated same asset does not consume pixels twice")
	var pins int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=? AND rev=2`, page).Scan(&pins); err != nil || pins != 8 {
		t.Fatalf("unique repeated refs pins%d %v", pins, err)
	}

	// A deliberately forged, encrypted bundle carries correct raster bytes,
	// correct SHA/metadata, matching canonical hashes and complete revision pins,
	// but exceeds only the decoded-pixel budget. Restore must independently refuse.
	for i := range state.Deck.Slides {
		if state.Deck.Slides[i].ID == slide.ID {
			state.Deck.Slides[i].Elements = append(state.Deck.Slides[i].Elements, ninth)
		}
	}
	canonical, err := slides.Canonical(state.Deck)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, err := slides.DeckHash(state.Deck)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `UPDATE slides_heads SET canonical_json=?,content_hash=? WHERE page_id=?`, canonical, contentHash, page)
	mustExec(t, f.db, `UPDATE slides_revisions SET canonical_json=?,content_hash=? WHERE page_id=? AND rev=2`, canonical, contentHash, page)
	mustExec(t, f.db, `UPDATE page_assets SET state='retained' WHERE id=?`, assets[8].ID)
	mustExec(t, f.db, `INSERT INTO slides_revision_assets(workspace_id,page_id,rev,asset_id,expected_sha256) VALUES('ws-old',?,2,?,?)`, page, assets[8].ID, assets[8].SHA256)
	const pass = "pixel-budget-forged-bundle-password"
	made, err := backup.CreateBackup(t.Context(), f.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: "ws-old", OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: f.r.pages.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	target := setupTestDB(t)
	_, err = backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: made.Path, Passphrase: pass, Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}, AttachmentRoot: storageDir(t)})
	if err == nil || !strings.Contains(err.Error(), "pixel") {
		t.Fatalf("over-pixel valid-raster bundle restore: %v", err)
	}
	if err := target.QueryRow(`SELECT COUNT(*) FROM slides_heads`).Scan(&pins); err != nil || pins != 0 {
		t.Fatal("over-pixel restore committed head", err)
	}
}
