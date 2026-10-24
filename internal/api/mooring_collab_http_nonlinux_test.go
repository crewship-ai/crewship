//go:build !linux

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestMooringHTTPUnsupportedClockRefusesStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded-binary-fixture")
	raw := []byte("not executed: unsupported clock must reject startup")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	router, err := NewRouter(setupTestDB(t), "mooring-unsupported-clock-test-key", newTestLogger(), WithInternalToken("synthetic-mooring-clock-test-token"), WithMooringHTTP(MooringHTTPConfig{Enabled: true, Binary: path, BinarySHA256: hex.EncodeToString(sum[:])}))
	if router != nil {
		router.Shutdown()
	}
	if err == nil || router != nil {
		t.Fatal("unsupported suspend-aware clock returned a serving controller")
	}
}
