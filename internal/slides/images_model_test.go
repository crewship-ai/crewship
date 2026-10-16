package slides

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func imageModelElement() Element {
	return Element{ID: "image_one", Type: "image", X: 4, Y: 5, Width: 20, Height: 30, Image: &Image{AssetID: "asset_one", ExpectedSHA256: strings.Repeat("a", 64), Alt: "Sample česky"}}
}
func imageUpgrade(t *testing.T, state State, id string) PatchRequest {
	t.Helper()
	version := ImageSchemaVersion
	element := imageModelElement()
	hash, err := SlideHash(state.Deck.Slides[0])
	if err != nil {
		t.Fatal(err)
	}
	return PatchRequest{PageID: state.Deck.DeckID, SchemaVersion: state.SchemaVersion, BaseRev: state.Revision, MutationID: id, TargetSchemaVersion: &version, IfContentHash: state.ContentHash, Operations: []Operation{{Op: "insert_element", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Element: &element}}}
}
func TestImageModelClosedVersionAndIdentity(t *testing.T) {
	deck, _, v1hash := modelFixture(t)
	unchanged := deck.Slides[0].Elements[0]
	oldhash, _ := ElementHash(unchanged)
	deck.Slides[0].Elements = append(deck.Slides[0].Elements, imageModelElement())
	raw, _ := json.Marshal(deck)
	if _, err := Decode(raw); err == nil {
		t.Fatal("image admitted under schema1")
	}
	deck.SchemaVersion = ImageSchemaVersion
	raw, _ = json.Marshal(deck)
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}
	hash, _ := DeckHash(deck)
	if hash == v1hash {
		t.Fatal("schema2 lost domain separation")
	}
	same, _ := ElementHash(unchanged)
	if same != oldhash {
		t.Fatal("unchanged element hash changed")
	}
	imagehash, _ := ElementHash(imageModelElement())
	encoded, _ := canonicalValue(imageModelElement())
	if imagehash == hashBytes(encoded) {
		t.Fatal("image used v1 hashdomain")
	}
	for name, mutate := range map[string]func(*Deck){
		"remote asset": func(d *Deck) {
			d.Slides[0].Elements[len(d.Slides[0].Elements)-1].Image.AssetID = "https://example.invalid/a"
		},
		"uppercase digest": func(d *Deck) {
			d.Slides[0].Elements[len(d.Slides[0].Elements)-1].Image.ExpectedSHA256 = strings.Repeat("A", 64)
		},
		"alt overflow": func(d *Deck) {
			d.Slides[0].Elements[len(d.Slides[0].Elements)-1].Image.Alt = strings.Repeat("x", MaxImageAltBytes+1)
		},
		"wrong payload": func(d *Deck) { d.Slides[0].Elements[len(d.Slides[0].Elements)-1].Text = &Text{} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, err := Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if Validate(candidate) == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}
func TestImageUpgradeFencesAndMixedHistory(t *testing.T) {
	// Core model transaction qualification only; real asset publication is tested
	// separately through the production Page asset guard.
	store, db, key, deck, _ := storeFixture(t, Options{OnRevision: func(context.Context, *sql.Tx, Key, int64, Deck) error { return nil }})
	initializeStore(t, store, key, deck)
	state := currentState(t, store, key)
	request := imageUpgrade(t, state, "first_image")
	raw, _ := json.Marshal(request)
	if _, err := DecodePatch(raw); err != nil {
		t.Fatal(err)
	}
	stale := request
	stale.IfContentHash = strings.Repeat("b", 64)
	if _, err := store.Patch(t.Context(), key, stale, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("hash fence: %v", err)
	}
	missing := request
	missing.TargetSchemaVersion = nil
	missing.IfContentHash = ""
	raw, _ = json.Marshal(missing)
	if _, err := DecodePatch(raw); err == nil {
		t.Fatal("implicit upgrade accepted")
	}
	first, err := store.Patch(t.Context(), key, request, storeAuthority)
	if err != nil || first.Revision != 2 {
		t.Fatalf("upgrade: %v %v", first, err)
	}
	replay, err := store.Patch(t.Context(), key, request, storeAuthority)
	if err != nil || !replay.Duplicate || replay.Revision != 2 {
		t.Fatalf("replay: %v %v", replay, err)
	}
	if got := currentState(t, store, key); got.SchemaVersion != 2 || got.Deck.SchemaVersion != 2 {
		t.Fatal("persisted wrong schema")
	}
	for _, restore := range []RestoreRequest{
		{PageID: key.PageID, SchemaVersion: 2, BaseRev: 2, MutationID: "restore_v1", IfOrderVersion: 1, Revision: 1},
		{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "restore_v2", IfOrderVersion: 1, Revision: 2},
	} {
		if _, err := store.Restore(t.Context(), key, restore, storeAuthority); err != nil {
			t.Fatal(err)
		}
	}
	for rev, want := range map[int64]int{1: 1, 2: 2, 3: 1, 4: 2} {
		got, err := store.Read(t.Context(), key, rev, storeAuthority)
		if err != nil || got.SchemaVersion != want || got.Deck.SchemaVersion != want {
			t.Fatalf("rev%d: %v %v", rev, got, err)
		}
	}
	if _, err := db.Exec(`UPDATE slides_heads SET schema_version=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(t.Context(), key, 0, storeAuthority); err == nil {
		t.Fatal("row/deck version mismatch accepted")
	}
}
func TestImagesRequireTransactionGuard(t *testing.T) {
	store, _, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	request := imageUpgrade(t, currentState(t, store, key), "unguarded_image")
	if _, err := store.Patch(t.Context(), key, request, storeAuthority); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unguarded image: %v", err)
	}
	if state := currentState(t, store, key); state.Revision != 1 || state.SchemaVersion != 1 {
		t.Fatal("unguarded image changed head")
	}
}
