package api

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// encKeyOnce ensures ENCRYPTION_KEY is set once at package level so parallel
// tests can use encryption helpers without `t.Setenv()` (which forbids parallel).
var encKeyOnce sync.Once

// TestMain installs the test encryption key BEFORE any test runs.
//
// Why this is not left to the first caller of setTestEncryptionKeyParallelSafe:
// the key is process-wide state that lives for the whole binary, so whichever
// test happened to call for it first was silently configuring encryption for
// every later test. Under source order that first caller sorted early, and the
// fixtures that encrypt a secret without asking for a key (seedPinnedWebhook's
// signing secret, crew-template deploy's webhook secret) worked by accident.
// Under -shuffle=on they sort before it and fail with "no usable encryption key
// is configured" — a failure with no relationship to the test that reports it.
// Installing the key here makes the state identical for every order instead of
// asking each fixture to remember a setup call.
//
// Tests that need encryption to be *unavailable* still override it with
// t.Setenv("ENCRYPTION_KEY", ""), which restores this value afterwards.
func TestMain(m *testing.M) {
	installTestEncryptionKey()
	lowerBcryptCostForTests()
	os.Exit(m.Run())
}

// lowerBcryptCostForTests is the ONLY write to bcryptCost anywhere, and it
// happens before a single test runs — so no test observes the value change
// and no -race report can come out of it.
//
// Why it is needed: bcrypt is deliberately slow and its cost is exponential,
// so production's cost 12 is ~256x cost 4. This package hashes a real
// password on every signup, bootstrap, password reset, profile password
// change and public-page token, and burns a real compare against the
// equaliser hash on every unknown-email signin. Under -race, where blowfish's
// key schedule is instrumented on every array access, that arithmetic was the
// single largest line item in the test binary and it is what spent the
// `Go Race (internal/api)` 30-minute budget (#2031).
//
// bcrypt.MinCost is a genuine bcrypt hash, not a stub: the handlers still
// call GenerateFromPassword, the stored value is still a `$2a$` hash that
// only the right password verifies against, and
// TestSignup_StoresARealBcryptHashAtTheConfiguredCost pins that. What the
// tests stop paying for is the key-stretching, which is a property of the
// deployed system, not of the handler logic under test.
func lowerBcryptCostForTests() {
	bcryptCost = bcrypt.MinCost
}

func installTestEncryptionKey() {
	encKeyOnce.Do(func() {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			panic("api tests: generate encryption key: " + err.Error())
		}
		// os.Setenv (NOT t.Setenv) so that t.Parallel() can be used.
		// The env var stays set for the entire test binary lifetime.
		os.Setenv("ENCRYPTION_KEY", hex.EncodeToString(key))
	})
}

// setTestEncryptionKeyParallelSafe stays as the explicit, self-documenting way
// for a fixture to declare "this path encrypts". TestMain has already run it, so
// it is now a no-op — but a fixture that states the dependency keeps working if
// the key ever stops being installed process-wide.
func setTestEncryptionKeyParallelSafe(t *testing.T) {
	t.Helper()
	installTestEncryptionKey()
}
