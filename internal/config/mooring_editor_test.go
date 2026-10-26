package config

import "testing"

func TestMooringEditorSharesOriginIsolationWithWorkbookEditor(t *testing.T) {
	for _, tc := range []struct {
		name, studio, runtime, allowed string
		wantErr                        bool
	}{
		{"valid", "https://studio.example.test", "https://pages.example.test", "https://studio.example.test", false},
		{"opaque-origin-trusted", "https://studio.example.test", "https://pages.example.test", "null", true},
		{"page-runtime-on-studio", "https://studio.example.test", "https://studio.example.test", "", true},
		{"insecure-studio", "http://studio.example.test", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Mooring.Enabled = true
			cfg.Storage.PageStudioOrigin = tc.studio
			cfg.Storage.PageRuntimeOrigin = tc.runtime
			if cfg.GridCraft.Enabled || cfg.GridCraft.EditorEnabled {
				t.Fatal("fixture must exercise Mooring without enabling GridCraft")
			}
			err := validateGridCraftEditor(cfg, tc.allowed)
			if (err != nil) != tc.wantErr {
				t.Fatalf("shared editor origin validation: got %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
