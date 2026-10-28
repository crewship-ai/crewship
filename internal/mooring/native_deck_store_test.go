package mooring

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Storage fixture only: this synthetic archive and trusted callback do not
// qualify the original Deck engine. Original-CLI qualification is separate.
func deckStoreZIP(t *testing.T, text string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, p := range [][2]string{{"mimetype", "application/vnd.storyteller.deckcraft+zip"}, {"presentation.json", `{"fixture":"` + text + `"}`}, {"media/7", "\x00\xffasset"}} {
		w, err := z.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(p[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNativeDeckStoreBinaryReceiptRestoreAndCustody(t *testing.T) {
	s, leases, _, clock, scope, p := nativeStoreFixture(t)
	first, second := deckStoreZIP(t, "first"), deckStoreZIP(t, "second")
	s.config.Application = "deckcraft"
	s.config.Validate = func(_ context.Context, raw []byte) error {
		if !bytes.Equal(raw, first) && !bytes.Equal(raw, second) {
			return ErrNativeValidation
		}
		return nil
	}
	lease, r, err := s.Initialize(t.Context(), scope, p, first)
	if err != nil || r.Version != 1 {
		t.Fatalf("Deck initialization: %+v %v", r, err)
	}
	check := func(want []byte, version int64) {
		t.Helper()
		state, err := s.Read(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		file, ok := state.Manifest.Files["document.deckcraft"]
		if !ok || state.Manifest.EntryPoint != "document.deckcraft" || len(state.Manifest.Files) != 1 || file.MediaType != "application/vnd.storyteller.deckcraft+zip" || !bytes.Equal(file.Content, want) || state.LatestVersion != version {
			t.Fatal("binary native manifest changed format, content or version")
		}
	}
	check(first, 1)
	digest := sha256.Sum256(second)
	hash := "sha256:" + hex.EncodeToString(digest[:])
	receipt, err := s.SaveSnapshot(t.Context(), lease, 1, "deck-2", hash, second)
	if err != nil || receipt.Version != "2" || receipt.Durability != "node" || receipt.ContentHash != hash {
		t.Fatalf("Deck receipt: %+v %v", receipt, err)
	}
	check(second, 2)
	if _, err = s.SaveSnapshot(t.Context(), lease, 1, "stale", hash, second); !errors.Is(err, artifacts.ErrVersionConflict) {
		t.Fatalf("CAS accepted stale base: %v", err)
	}
	confirmed, err := s.ReconcileSnapshot(t.Context(), lease, 1, "deck-2", hash)
	if err != nil || confirmed != receipt {
		t.Fatalf("exact receipt reconciliation: %+v %v", confirmed, err)
	}
	if _, err = s.Restore(t.Context(), lease, 2, 1); err != nil {
		t.Fatal(err)
	}
	check(first, 3)
	clock.advance(5 * time.Second)
	if _, err = leases.Acquire(t.Context(), scope, EditLeasePrincipal{"u", "new-deck-tab"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveSnapshot(t.Context(), lease, 3, "late", hash, second); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("old Deck writer accepted: %v", err)
	}
}
