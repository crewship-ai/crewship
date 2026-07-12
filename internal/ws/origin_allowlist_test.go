package ws

import "testing"

// TestHandleUpgrade_AllowlistedOrigin locks the desktop-shell contract: the
// WS upgrade honors CREWSHIP_ALLOWED_ORIGINS — the same allowlist the HTTP
// layer's EnforceOrigin consults — even in production, where the hardcoded
// policy would otherwise reject every non-same-host origin. Without this a
// Tauri shell (Origin: tauri://localhost / http://tauri.localhost) can make
// authed HTTP calls but never open the realtime socket.
//
// No t.Parallel(): t.Setenv is process-global.
func TestHandleUpgrade_AllowlistedOrigin(t *testing.T) {
	t.Setenv("CREWSHIP_ENV", "production")

	t.Run("allowlisted desktop origin accepted in production", func(t *testing.T) {
		t.Setenv("CREWSHIP_ALLOWED_ORIGINS", "tauri://localhost,http://tauri.localhost")
		if err := originDial(t, "http://tauri.localhost"); err != nil {
			t.Errorf("allowlisted origin rejected: %v", err)
		}
		if err := originDial(t, "tauri://localhost"); err != nil {
			t.Errorf("allowlisted custom-scheme origin rejected: %v", err)
		}
	})

	t.Run("trailing slash in allowlist entry still matches", func(t *testing.T) {
		t.Setenv("CREWSHIP_ALLOWED_ORIGINS", "http://tauri.localhost/")
		if err := originDial(t, "http://tauri.localhost"); err != nil {
			t.Errorf("allowlisted origin (trailing-slash entry) rejected: %v", err)
		}
	})

	t.Run("non-allowlisted origin still rejected in production", func(t *testing.T) {
		t.Setenv("CREWSHIP_ALLOWED_ORIGINS", "http://tauri.localhost")
		if err := originDial(t, "http://evil.example.com"); err == nil {
			t.Error("non-allowlisted origin must stay rejected")
		}
	})

	t.Run("localhost bypass stays disabled in production", func(t *testing.T) {
		t.Setenv("CREWSHIP_ALLOWED_ORIGINS", "")
		if err := originDial(t, "http://localhost:3000"); err == nil {
			t.Error("production must reject localhost origins absent an allowlist entry")
		}
	})
}
