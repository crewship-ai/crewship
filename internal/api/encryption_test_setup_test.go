package api

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/testutil"
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
	cleanupHome := isolateHomeForTests()
	// Two workers, four ready copies: enough to keep a sequential run fed on
	// a four-core CI runner without holding more than a few extra open
	// databases. See internal/testutil/migrateddb_prefetch.go for why the
	// per-test open is worth moving off the critical path at all.
	stopPrefetch := testutil.PrefetchMigratedDBs(2, 4)
	code := m.Run()
	stopPrefetch()
	cleanupHome()
	os.Exit(code)
}

// isolateHomeForTests points HOME at an empty directory for the whole test
// binary and clears CREWSHIP_DATA_DIR, so a handler that resolves the default
// data directory (database.DefaultDataDir: $CREWSHIP_DATA_DIR, else
// ~/.crewship) sees an empty one rather than the developer's.
//
// Before this, any test that served an admin route through NewRouter without
// sandboxing HOME itself read the real ~/.crewship. On crewship-dev that is
// 8.4 GB of backup bundles, and TestAdminFloor_MemberDeniedAdminSurface spent
// 122 s of a 465 s package run decompressing them to answer GET
// /api/v1/admin/backups — while DefaultDataDir also created
// ~/.crewship/{output,chats,logs,skills} as a side effect. On a CI runner the
// directory is empty, so this changes nothing there except that the result no
// longer depends on the machine.
//
// Tests that need a particular HOME or data dir still t.Setenv their own,
// which restores this value afterwards.
func isolateHomeForTests() (cleanup func()) {
	home, err := os.MkdirTemp("", "crewship-api-test-home-")
	if err != nil {
		panic("api tests: create isolated HOME: " + err.Error())
	}
	os.Setenv("HOME", home)
	os.Unsetenv("CREWSHIP_DATA_DIR")
	return func() { _ = os.RemoveAll(home) }
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
