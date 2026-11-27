//go:build craftpackages

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftassets"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// With real release packages (CREWSHIP_CRAFT_TEST_PACKAGES=<directory with
// catalog.txt and the .craftpkg files of scripts/build-craft-components.py>),
// both install from their files: the editor origin verifies and serves each
// real editor (including its CSP and WebRTC checks) and each real WASM engine
// answers. It is built only with -tags craftpackages:
//
//	CREWSHIP_CRAFT_TEST_PACKAGES=/abs/dist go test -tags craftpackages ./internal/server -run ReleaseCraftPackages
func TestReleaseCraftPackagesInstallAndRun(t *testing.T) {
	dir := os.Getenv("CREWSHIP_CRAFT_TEST_PACKAGES")
	if dir == "" {
		t.Fatal("set CREWSHIP_CRAFT_TEST_PACKAGES to the directory of the release packages")
	}
	pins, err := os.ReadFile(filepath.Join(dir, "catalog.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.CraftNative.ComponentsDir = t.TempDir()
	cfg.CraftNative.ComponentPins = strings.TrimSpace(string(pins))
	logger := slog.New(slog.DiscardHandler)
	craft := resolveCraftApps(cfg, logger)
	editor := newEditorOrigin(cfg, craft, logger)
	out, err := nativeCraftHTTPConfig(cfg, craft, editor)
	if err != nil || out.Install == nil {
		t.Fatalf("native config: %v", err)
	}
	for _, app := range craftapps.Applications {
		f, err := os.Open(filepath.Join(dir, craftassets.AssetName(app)))
		if err != nil {
			t.Fatal(err)
		}
		p, c, err := out.Install(context.Background(), app, f)
		f.Close()
		if err != nil || !editor.HasBundle(c.EditorBundle) {
			t.Fatalf("%s: %v", app, err)
		}
		var stdout bytes.Buffer
		if err := p.Engine.Run(context.Background(), t.TempDir(), []string{"--version"}, &stdout, &bytes.Buffer{}); err != nil || !strings.Contains(stdout.String(), app) {
			t.Fatalf("%s engine: %q %v", app, stdout.String(), err)
		}
		t.Logf("%s %s: %s", app, c.Version, strings.TrimSpace(stdout.String()))
		if p.Workbook {
			t.Run("LegacySheetRoundTrip", func(t *testing.T) {
				source, err := os.ReadFile("../../lib/mooring/gridcraft-empty.ts")
				if err != nil {
					t.Fatal(err)
				}
				_, literal, found := strings.Cut(string(source), "const emptyWorkbook = ")
				literal, _, boundary := strings.Cut(literal, "\n\nexport function")
				var raw string
				if !found || !boundary || json.Unmarshal([]byte(strings.TrimSpace(literal)), &raw) != nil {
					t.Fatal("missing canonical legacy Sheet JSON")
				}
				if _, err := mooring.ConvertLegacyWorkbook(t.Context(), p.Engine, []byte(raw)); err != nil {
					t.Fatalf("canonical legacy Sheet: %v", err)
				}
			})
		}
	}
}
