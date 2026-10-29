//go:build !linux

package api

import (
	"strings"
	"testing"
)

func TestCraftNativeUnsupportedPlatformFailsClosed(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	router, err := NewRouter(f.db, "native-route-fixture-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if router != nil {
		router.Shutdown()
	}
	if err == nil || !strings.Contains(err.Error(), "Linux CLOCK_BOOTTIME") {
		t.Fatalf("unsupported native owner clock was not refused: %v", err)
	}
}
