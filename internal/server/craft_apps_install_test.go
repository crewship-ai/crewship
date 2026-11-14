package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftassets"
	"github.com/crewship-ai/crewship/internal/craftassets/craftassetstest"
)

// Word and Sheet are not in the binary (#5315). A release trusts one package
// of each; with no configuration the server starts with both "not installed".
// Installing (downloaded, or from a file) verifies the package, serves its
// editor at its pin behind the native credential gate and runs its WASM
// engine, all without a restart; after a restart the installed package is
// found again. A build that trusts no package offers nothing.
func TestCraftApplicationsInstallWithoutRestart(t *testing.T) {
	word := craftassetstest.Build("wordcraft", "0.11.0+b0fb5cb571b6", "Word")
	sheet := craftassetstest.Build("gridcraft", "0.4.0+e68d19220a1b", "Sheet")
	var downloads int
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0.0/"+craftassets.AssetName("wordcraft") {
			downloads++
			_, _ = w.Write(word.Archive)
			return
		}
		http.NotFound(w, r)
	}))
	defer mirror.Close()

	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.CraftNative.ComponentsDir = t.TempDir()
	cfg.CraftNative.ComponentPins = craftassets.String(map[string]craftassets.Pin{"wordcraft": word.Pin, "gridcraft": sheet.Pin})
	cfg.CraftNative.ComponentURL = mirror.URL + "/v1.0.0/"
	logger := slog.New(slog.DiscardHandler)

	craft := resolveCraftApps(cfg, logger)
	for _, app := range craftapps.Applications {
		if c := craft.components[app]; c.Available || c.Reason != craftapps.ReasonNotInstalled || !c.Development {
			t.Fatalf("%s before install = %+v", app, c)
		}
	}
	editor := newEditorOrigin(cfg, craft, logger)
	if editor == nil {
		t.Fatal("no editor origin while applications can be installed")
	}
	out, err := nativeCraftHTTPConfig(cfg, craft, editor)
	if err != nil || !out.Enabled || out.Install == nil || len(out.Profiles) != 0 {
		t.Fatalf("native config before install: %+v %v", out, err)
	}
	// The gate is built at startup, before anything is installed.
	gate := nativeCraftCredentialGate(cfg, craft, editor, editor)
	withCookie := func(digest string) int {
		r := httptest.NewRequest("GET", "http://127.0.0.1/_editor/b/"+digest+"/index.html", nil)
		r.Header.Set("Cookie", "crewship_session=x")
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w.Code
	}
	if withCookie(word.Editor) == http.StatusForbidden {
		t.Fatal("an editor that is not installed is fenced")
	}

	ctx := context.Background()
	p, c, err := out.Install(ctx, "wordcraft", nil)
	if err != nil || downloads != 1 {
		t.Fatalf("install Word: %v (downloads %d)", err, downloads)
	}
	// The pins come from configuration, not from this release: Admin and the
	// CLI show the package as an operator's (development) override.
	if !c.Available || !c.Development || c.Version != word.Pin.Version || c.EngineSHA256 != word.EngineSHA256 || c.EditorBundle != word.Editor {
		t.Fatalf("Word component = %+v", c)
	}
	if p.EditorURL != "/_editor/b/"+word.Editor+"/index.html" || !editor.HasBundle(word.Editor) || !craft.fenced(word.Editor) {
		t.Fatalf("Word editor not served at its pin: %+v", p)
	}
	if code := withCookie(word.Editor); code != http.StatusForbidden {
		t.Fatalf("installed editor with credentials = %d, want 403", code)
	}
	var stdout bytes.Buffer
	if err = p.Engine.Run(ctx, t.TempDir(), []string{"--version"}, &stdout, &bytes.Buffer{}); err != nil || stdout.String() != "wordcraft-cli 0.11.0+b0fb5cb571b6\n" {
		t.Fatalf("Word engine: %q %v", stdout.String(), err)
	}

	// Offline: Sheet from a file. The Word package is not Sheet's.
	if _, _, err = out.Install(ctx, "gridcraft", bytes.NewReader(word.Archive)); err == nil {
		t.Fatal("the Word package was installed as Sheet")
	}
	if _, _, err = out.Install(ctx, "gridcraft", bytes.NewReader(sheet.Archive)); err != nil || !editor.HasBundle(sheet.Editor) {
		t.Fatalf("install Sheet from a file: %v", err)
	}

	// A restart finds both again, verified, without downloading.
	again := resolveCraftApps(cfg, logger)
	editor2 := newEditorOrigin(cfg, again, logger)
	out2, err := nativeCraftHTTPConfig(cfg, again, editor2)
	if err != nil || len(out2.Profiles) != 2 || downloads != 1 {
		t.Fatalf("after restart: %d profiles, %v, downloads %d", len(out2.Profiles), err, downloads)
	}
	for app, pkg := range map[string]craftassetstest.Package{"wordcraft": word, "gridcraft": sheet} {
		if s := out2.Components[app]; !s.Available || !s.Development || !editor2.HasBundle(pkg.Editor) || out2.Profiles[app].EditorBundle != pkg.Editor {
			t.Fatalf("%s after restart = %+v", app, s)
		}
	}

	// A build that trusts no package: nothing to install, nothing wired.
	plain := config.Default()
	plain.CraftNative.ComponentsDir = cfg.CraftNative.ComponentsDir
	none := resolveCraftApps(plain, logger)
	if none.enabled() || none.components["wordcraft"].Reason != craftapps.ReasonComponentMissing {
		t.Fatalf("no catalog = %+v", none.components)
	}
	if out3, _ := nativeCraftHTTPConfig(plain, none, newEditorOrigin(plain, none, logger)); out3.Enabled || out3.Install != nil {
		t.Fatalf("no catalog wired: %+v", out3)
	}
	if !strings.Contains(craftassets.String(again.catalog.Pins), word.Pin.ManifestSHA256) {
		t.Fatal("catalog lost the pin")
	}
}
