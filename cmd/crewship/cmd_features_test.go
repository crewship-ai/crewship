package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestFeaturesCmdStructure(t *testing.T) {
	t.Parallel()

	if featuresCmd.Use != "features" {
		t.Errorf("features Use: got %q", featuresCmd.Use)
	}

	have := map[string]bool{}
	for _, sub := range featuresCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "info", "base-images"} {
		if !have[want] {
			t.Errorf("features missing subcommand %q; have %v", want, have)
		}
	}
}

// TestBaseImagesCatalogIntegrity pins the static catalog so a thoughtless
// reorder or typo doesn't silently deliver a broken devcontainer image to
// new crews. The list mirrors the frontend copy in runtime-config.tsx; if
// they drift the CLI operator sees one menu and the UI user sees another.
func TestBaseImagesCatalogIntegrity(t *testing.T) {
	t.Parallel()

	if len(baseImagesCatalog) == 0 {
		t.Fatal("baseImagesCatalog is empty")
	}

	seen := map[string]bool{}
	recommended := 0
	for i, b := range baseImagesCatalog {
		if b.Image == "" {
			t.Errorf("row %d has empty Image", i)
		}
		if b.Label == "" {
			t.Errorf("row %d (%q) has empty Label", i, b.Image)
		}
		if b.Description == "" {
			t.Errorf("row %d (%q) has empty Description", i, b.Image)
		}
		if !strings.HasPrefix(b.Image, "mcr.microsoft.com/") {
			t.Errorf("row %d image should come from MCR; got %q", i, b.Image)
		}
		if seen[b.Image] {
			t.Errorf("duplicate image %q", b.Image)
		}
		seen[b.Image] = true
		if b.Recommended {
			recommended++
		}
	}

	// Exactly one recommended entry keeps the UX unambiguous — N>=2 shows
	// "RECOMMENDED" on multiple rows and the operator has no way to pick.
	if recommended != 1 {
		t.Errorf("expected exactly 1 recommended entry; got %d", recommended)
	}
}

func TestBaseImagesCatalog_ContainsCommonRuntimes(t *testing.T) {
	t.Parallel()

	// Sanity: the catalog should cover the major stacks Crewship ships.
	wantSubstr := []string{"javascript-node", "python", "go", "rust", "base", "universal"}
	for _, w := range wantSubstr {
		found := false
		for _, b := range baseImagesCatalog {
			if strings.Contains(b.Image, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("catalog should cover %q-based image", w)
		}
	}
}

func TestFeaturesListFlags(t *testing.T) {
	t.Parallel()

	search := featuresListCmd.Flags().Lookup("search")
	if search == nil {
		t.Fatal("features list missing --search flag")
	}
	if search.DefValue != "" {
		t.Errorf("--search default: got %q want empty", search.DefValue)
	}
}

func TestFeaturesListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := featuresListCmd.RunE(featuresListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

// featuresMock serves /api/v1/features/catalog with a configurable payload
// and captures the last URL called so tests can assert --search propagation.
type featuresMock struct {
	t           *testing.T
	lastURL     string
	status      int
	payload     []byte
}

func (m *featuresMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.lastURL = r.URL.RequestURI()
		if m.status != 0 {
			w.WriteHeader(m.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(m.payload)
	})
}

func TestFeaturesListRunE_OmitsSearchWhenEmpty(t *testing.T) {
	saveCLIState(t)
	m := &featuresMock{t: t, payload: []byte(`{"features":[]}`)}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}
	_ = featuresListCmd.Flags().Set("search", "")
	t.Cleanup(func() { _ = featuresListCmd.Flags().Set("search", "") })

	if err := featuresListCmd.RunE(featuresListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if !strings.HasPrefix(m.lastURL, "/api/v1/features/catalog") {
		t.Fatalf("unexpected path %q", m.lastURL)
	}
	if strings.Contains(m.lastURL, "search=") {
		t.Errorf("search= should be absent when flag empty; got %q", m.lastURL)
	}
}

func TestFeaturesListRunE_PropagatesSearchWithURLEncoding(t *testing.T) {
	saveCLIState(t)
	m := &featuresMock{t: t, payload: []byte(`{"features":[]}`)}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}
	if err := featuresListCmd.Flags().Set("search", "node & python"); err != nil {
		t.Fatalf("set --search: %v", err)
	}
	t.Cleanup(func() { _ = featuresListCmd.Flags().Set("search", "") })

	if err := featuresListCmd.RunE(featuresListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// url.QueryEscape: space → "+", & → "%26".
	if !strings.Contains(m.lastURL, "search=node+%26+python") {
		t.Errorf("search not properly encoded; got %q", m.lastURL)
	}
}

func TestFeaturesInfoArgsValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		args    []string
		wantErr bool
	}{
		{[]string{}, true},
		{[]string{"ghcr.io/devcontainers/features/node"}, false},
		{[]string{"a", "b"}, true},
	} {
		err := featuresInfoCmd.Args(featuresInfoCmd, tc.args)
		if tc.wantErr && err == nil {
			t.Errorf("args=%v: expected error", tc.args)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("args=%v: expected no error, got %v", tc.args, err)
		}
	}
}

func TestFeaturesInfoRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := featuresInfoCmd.RunE(featuresInfoCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestFeaturesInfoRunE_UnknownRefError(t *testing.T) {
	saveCLIState(t)

	body, _ := json.Marshal(map[string]any{
		"features": []map[string]string{
			{"ref": "ghcr.io/devcontainers/features/node", "name": "Node", "category": "language", "size_hint": "small"},
		},
	})
	m := &featuresMock{t: t, payload: body}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}

	err := featuresInfoCmd.RunE(featuresInfoCmd, []string{"ghcr.io/devcontainers/features/ghost"})
	if err == nil || !strings.Contains(err.Error(), "feature not found") {
		t.Errorf("expected 'feature not found'; got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the requested ref; got %v", err)
	}
}

func TestFeaturesInfoRunE_FoundRefSucceeds(t *testing.T) {
	saveCLIState(t)

	body, _ := json.Marshal(map[string]any{
		"features": []map[string]string{
			{"ref": "ghcr.io/devcontainers/features/node", "name": "Node", "category": "language", "description": "Node.js runtime", "icon": "node", "size_hint": "small"},
		},
	})
	m := &featuresMock{t: t, payload: body}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}

	if err := featuresInfoCmd.RunE(featuresInfoCmd, []string{"ghcr.io/devcontainers/features/node"}); err != nil {
		t.Errorf("RunE: %v", err)
	}
}

// features base-images renders purely from the static catalog — no auth
// required, no network call. Exercise the RunE so regressions like an
// accidental requireAuth() addition would surface.
func TestFeaturesBaseImagesRunE_NoAuthNoNetwork(t *testing.T) {
	saveCLIState(t)
	// Deliberately NOT setting cliCfg — command must not reach requireAuth.

	if err := featuresBaseImagesCmd.RunE(featuresBaseImagesCmd, nil); err != nil {
		t.Errorf("RunE: %v", err)
	}
}
