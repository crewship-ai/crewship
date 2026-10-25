package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/mooringassets"
)

func TestMooringServerRuntimeAssetsGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			cfg := silentCfg()
			cfg.Storage.BasePath = t.TempDir()
			cfg.Storage.LogPath = t.TempDir()
			cfg.Storage.MemoryRoot = t.TempDir()
			cfg.Storage.PageStudioOrigin = "https://studio.example.test"
			cfg.Mooring.Enabled = enabled
			s := New(cfg, logging.New("error", "json", nil), &Deps{DB: openTestDB(t)})
			t.Cleanup(func() {
				var owned string
				if s.mooringAssets != nil {
					owned = s.mooringAssets.Path
				}
				_ = s.Shutdown()
				if owned != "" {
					if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("owned runtime executable survived shutdown: %v", err)
					}
				}
			})
			if !enabled {
				if s.mooringAssets != nil || s.mooringStartupErr != nil {
					t.Fatal("default-off extracted or required runtime assets")
				}
				return
			}
			_, embeddedErr := mooringassets.Inspect()
			if embeddedErr != nil {
				if s.mooringStartupErr == nil {
					t.Fatal("enabled server accepted missing/corrupt runtime assets")
				}
				if err := s.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "Mooring runtime startup") {
					t.Fatalf("startup did not fail before listeners: %v", err)
				}
				return
			}
			if s.mooringStartupErr != nil || s.mooringAssets == nil || s.apiRouter == nil {
				t.Fatalf("verified embedded startup failed: %v", s.mooringStartupErr)
			}
			executable, err := os.Stat(s.mooringAssets.Path)
			if err != nil || executable.Mode().Perm() != 0o500 {
				t.Fatalf("private executable mode: %v / %v", executable, err)
			}
			directory, err := os.Stat(filepath.Dir(s.mooringAssets.Path))
			if err != nil || directory.Mode().Perm() != 0o700 {
				t.Fatalf("private directory mode: %v / %v", directory, err)
			}
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/pages/hidden/mooring/session", nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("verified enabled route: got %d, want401", w.Code)
			}
		})
	}
}

func TestMooringServerInvalidLeaseFailsBeforeExtraction(t *testing.T) {
	cfg := silentCfg()
	cfg.Mooring.Enabled = true
	cfg.Mooring.DeliveryLeaseMilliseconds = -1
	s := &Server{cfg: cfg}
	if opts := s.configureMooringHTTP(nil); len(opts) != 0 || s.mooringStartupErr == nil || s.mooringAssets != nil {
		t.Fatal("invalid lease reached assets or routes")
	}
	if err := s.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "Mooring runtime startup") {
		t.Fatalf("invalid lease startup: %v", err)
	}
}
