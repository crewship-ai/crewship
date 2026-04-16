package api

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
)

// encKeyOnce ensures ENCRYPTION_KEY is set once at package level so parallel
// tests can use encryption helpers without `t.Setenv()` (which forbids parallel).
var encKeyOnce sync.Once

func setTestEncryptionKeyParallelSafe(t *testing.T) {
	t.Helper()
	encKeyOnce.Do(func() {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("rand: %v", err)
		}
		// os.Setenv (NOT t.Setenv) so that t.Parallel() can be used.
		// The env var stays set for the entire test binary lifetime.
		os.Setenv("ENCRYPTION_KEY", hex.EncodeToString(key))
	})
}
