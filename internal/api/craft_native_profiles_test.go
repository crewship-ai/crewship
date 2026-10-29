package api

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func craftNativeProfileFixture(t *testing.T) CraftNativeDocumentProfile {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fixture-cli")
	raw := []byte("#!/bin/sh\nexit 9\n")
	if err := os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	return CraftNativeDocumentProfile{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), CLIPath: binary, CLISHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), EditorBundle: strings.Repeat("c", 64), EditorURL: "/_editor/b/" + strings.Repeat("c", 64) + "/index.html"}
}
func TestCraftNativeDocumentProfileOptionCapturesImmutableMap(t *testing.T) {
	original := craftNativeProfileFixture(t)
	input := map[string]CraftNativeDocumentProfile{"wordcraft": original}
	option := WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true, Profiles: input})
	input["wordcraft"] = CraftNativeDocumentProfile{Application: "gridcraft"}
	var router Router
	option(&router)
	if router.craftNativeHTTPConfig.Profiles["wordcraft"] != original {
		t.Fatal("startup profile adopted external map mutation")
	}
	profiles, err := qualifyCraftNativeProfiles(router.craftNativeHTTPConfig.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	if profiles["wordcraft"].Store.Validate == nil || profiles["wordcraft"].Store.EngineSHA256 != original.CLISHA256 {
		t.Fatal("profile has no pinned production validator")
	}
}
func TestCraftNativeDocumentProfileRefusesUnqualifiedExecutablePinsAndURLs(t *testing.T) {
	original := craftNativeProfileFixture(t)
	for _, tc := range []struct {
		name   string
		mutate func(*CraftNativeDocumentProfile)
	}{
		{"application mismatch", func(p *CraftNativeDocumentProfile) { p.Application = "gridcraft" }},
		{"engine revision", func(p *CraftNativeDocumentProfile) { p.EngineRevision = "main" }},
		{"relative executable", func(p *CraftNativeDocumentProfile) { p.CLIPath = "cli" }},
		{"different executable hash", func(p *CraftNativeDocumentProfile) { p.CLISHA256 = strings.Repeat("b", 64) }},
		{"missing executable", func(p *CraftNativeDocumentProfile) { p.CLIPath = filepath.Join(t.TempDir(), "absent") }},
		{"wrong bundle", func(p *CraftNativeDocumentProfile) {
			p.EditorURL = "/_editor/b/" + strings.Repeat("d", 64) + "/index.html"
		}},
		{"javascript URL", func(p *CraftNativeDocumentProfile) { p.EditorURL = "javascript:alert(1)" }},
		{"query URL", func(p *CraftNativeDocumentProfile) { p.EditorURL += "?token=private" }},
		{"ambiguous encoded URL", func(p *CraftNativeDocumentProfile) { p.EditorURL = "/_editor/b/" + p.EditorBundle + "/%69ndex.html" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := original
			tc.mutate(&candidate)
			if _, err := qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{"wordcraft": candidate}); err == nil {
				t.Fatal("accepted unqualified native profile")
			}
		})
	}
}
func TestCraftNativeDocumentProfileInvalidStartupReturnsErrorAndDisabledIgnoresIt(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	profile := craftNativeProfileFixture(t)
	profile.CLIPath = "relative"
	opts := CraftNativeHTTPConfig{Enabled: true, Profiles: map[string]CraftNativeDocumentProfile{"wordcraft": profile}}
	if router, err := NewRouter(f.db, "native-invalid-profile-secret32chars", newTestLogger(), WithCraftNativeHTTP(opts)); err == nil {
		router.Shutdown()
		t.Fatal("invalid enabled startup succeeded")
	}
	opts.Enabled = false
	router, err := NewRouter(f.db, "native-disabled-profile-secret32chars", newTestLogger(), WithCraftNativeHTTP(opts))
	if err != nil {
		t.Fatalf("disabled profile touched invalid executable: %v", err)
	}
	router.Shutdown()
}
