//go:build linux && (slides_images_browser || conformance)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Real private raster bytes with distinct owner WRITE and reader READ identities.
func TestSlidesImagesBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_SLIDES_IMAGES_BROWSER") != "1" {
		t.Fatal("requires explicit browser opt-in, production export and Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build production export first", err)
	}
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	seedInstanceUser(t, f.db, "slide-reader", "reader@example.invalid", "ws-old", "MEMBER")
	validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"boss", "slide-reader"} {
		session, e := sessions.NewDBStore(f.db).Create(t.Context(), user, "synthetic-images-browser", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
		if e != nil {
			t.Fatal(e)
		}
	}
	wmSeedPage(t, f.db, "ws-old", "images-native-page", "images-native", "Native images", "boss", "")
	deck := slideTestDeck(t, "images-native-page")
	wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/images-native/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "images-init", Deck: deck})), 200, "initialize")
	execOrFatal(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('images-native-page','user','slide-reader','read','boss')`)
	f.r.pages.artifactStoragePath = storageDir(t)
	imagePath := filepath.Join(t.TempDir(), "owned-image.png")
	raster := image.NewRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 96; x++ {
			raster.Set(x, y, color.RGBA{uint8(x * 255 / 95), uint8(y * 255 / 63), 140, 255})
		}
	}
	var encoded bytes.Buffer
	if err = png.Encode(&encoded, raster); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(imagePath, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", f.r)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]any{"server": server.URL, "workspace": "ws-old", "owner_token": tokens["boss"], "reader_token": tokens["slide-reader"], "image_path": imagePath})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "images-browser.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-slides-images-browser.mjs"))
	cmd.Env = append(os.Environ(), "CREWSHIP_SLIDES_IMAGES_FIXTURE="+path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		t.Fatal("actual native image browser", err)
	}
}
