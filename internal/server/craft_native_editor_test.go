package server

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func nativeEditorConfigFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	cfg := config.Default()
	dir := t.TempDir()
	bundle := writeEditorBundle(t, dir)
	binary := filepath.Join(t.TempDir(), "fixture-cli")
	raw := []byte("#!/bin/sh\nexit 9\n")
	if err := os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.JWTSecret = "native-editor-server-fixture-secret-32chars"
	cfg.CraftNative = config.CraftNativeConfig{Enabled: true, EditorBundleDir: dir, Profiles: map[string]config.CraftNativeDocumentProfile{"wordcraft": {Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), CLIPath: binary, CLISHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), EditorBundle: bundle, EditorURL: "/_editor/b/" + bundle + "/index.html"}}}
	return cfg, bundle
}
func TestNativeEditorIncompatibleC1DirectoryFailsClosed(t *testing.T) {
	cfg, _ := nativeEditorConfigFixture(t)
	other := t.TempDir()
	old := writeEditorBundle(t, other)
	cfg.GridCraft.Enabled = true
	cfg.GridCraft.EditorEnabled = true
	cfg.GridCraft.EditorBundleDir = other
	cfg.GridCraft.EditorBundles = []string{old}
	editor := newEditorOrigin(cfg, slog.Default())
	if editor != nil && editor.HasBundle(old) {
		t.Fatal("incompatible native directory silently retained or replaced C1 mount")
	}
}

func TestNativeDocumentServerOnlyPublishesVerifiedServedBundleMetadata(t *testing.T) {
	cfg, pin := nativeEditorConfigFixture(t)
	editor := newEditorOrigin(cfg, slog.Default())
	out, err := nativeCraftHTTPConfig(cfg, editor)
	if err != nil {
		t.Fatal(err)
	}
	expected := out.Profiles["wordcraft"]
	profile := cfg.CraftNative.Profiles["wordcraft"]
	profile.EditorURL = "/_editor/b/" + strings.Repeat("f", 64) + "/index.html"
	cfg.CraftNative.Profiles["wordcraft"] = profile
	if out.Profiles["wordcraft"] != expected {
		t.Fatal("server mapped profile adopted caller mutation")
	}
	if _, err = nativeCraftHTTPConfig(cfg, editor); err == nil {
		t.Fatal("server published mismatched served editor URL")
	}
	profile.EditorURL = "/_editor/b/" + pin + "/index.html"
	cfg.CraftNative.Profiles["wordcraft"] = profile
	if _, err = nativeCraftHTTPConfig(cfg, nil); err == nil {
		t.Fatal("server published unavailable editor bundle")
	}
}
