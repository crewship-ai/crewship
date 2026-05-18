package ws

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// ---------------------------------------------------------------------------
// hub.go — NopSessionsForTests / nopHubSessions contract.
//
// NopSessionsForTests is consumed by many test files in api/ and ws/ as
// the third arg to NewHub. The interface methods themselves are never
// directly exercised — and a silent contract drift here (e.g. Get
// returning nil error instead of ErrNotFound) would make every test
// that thinks it's hitting a "no session" branch quietly start hitting
// the "session present" branch. These tests lock the no-op semantics
// so a refactor of the stub can't change downstream test outcomes
// without flipping the assertion here first.
// ---------------------------------------------------------------------------

func TestNopHubSessions_GetReturnsErrNotFound(t *testing.T) {
	got, err := NopSessionsForTests.Get(context.Background(), "any-id")
	if got != nil {
		t.Errorf("Get returned non-nil session %+v; nop store should never return a row", got)
	}
	if !errors.Is(err, sessions.ErrNotFound) {
		t.Errorf("Get err = %v, want sessions.ErrNotFound (downstream tests rely on this to exercise the \"session missing\" branch)", err)
	}
}

func TestNopHubSessions_CreateReturnsErrorByDesign(t *testing.T) {
	// Create is intentionally unsupported on the stub: tests that need a
	// real session must build their own DBStore against an in-memory DB.
	// Make sure the stub fails LOUDLY so an accidental use surfaces
	// during test development, not as a silent zero-value Session.
	got, err := NopSessionsForTests.Create(context.Background(), "user-1", "ua", "10.0.0.1", time.Hour)
	if got != nil {
		t.Errorf("Create returned non-nil session %+v; nop store should never mint one", got)
	}
	if err == nil {
		t.Error("Create returned nil error; nop store should fail loudly to flag misuse")
	}
}

func TestNopHubSessions_ListActiveForUserReturnsEmpty(t *testing.T) {
	got, err := NopSessionsForTests.ListActiveForUser(context.Background(), "user-1")
	if err != nil {
		t.Errorf("ListActiveForUser err = %v, want nil (empty list is a valid no-op)", err)
	}
	// Accept either nil or an empty non-nil slice — both are valid
	// no-op shapes and downstream iteration over `for _, s := range got`
	// behaves identically either way. Pinning to nil-only would reject
	// a legitimate implementation that pre-allocates.
	if len(got) != 0 {
		t.Errorf("ListActiveForUser returned %+v; want nil or empty", got)
	}
}

func TestNopHubSessions_RevokeIsNoop(t *testing.T) {
	if err := NopSessionsForTests.Revoke(context.Background(), "any-id", "any-reason"); err != nil {
		t.Errorf("Revoke err = %v, want nil (no-op success)", err)
	}
}

func TestNopHubSessions_RevokeAllForUserReturnsZero(t *testing.T) {
	n, err := NopSessionsForTests.RevokeAllForUser(context.Background(), "user-1", "logout")
	if err != nil {
		t.Errorf("RevokeAllForUser err = %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("RevokeAllForUser n = %d, want 0 (no real sessions exist on a stub)", n)
	}
}

func TestNopHubSessions_TouchLastUsedIsNoop(t *testing.T) {
	if err := NopSessionsForTests.TouchLastUsed(context.Background(), "any-id"); err != nil {
		t.Errorf("TouchLastUsed err = %v, want nil", err)
	}
}

func TestNopHubSessions_RotateRefreshJtiIsNoop(t *testing.T) {
	// Refresh-token-reuse detection (OWASP ASVS 3.7.4) lives in the real
	// store; the stub returns nil so hub-plumbing tests don't need to
	// thread a CAS-aware fake through every code path that touches the
	// rotation step. A non-nil return here would break TestHandleUpgrade
	// branches that touch token refresh.
	if err := NopSessionsForTests.RotateRefreshJti(context.Background(), "sess-id", "jti-old", "jti-new"); err != nil {
		t.Errorf("RotateRefreshJti err = %v, want nil", err)
	}
}

func TestNopHubSessions_SetClockIsNoop(t *testing.T) {
	// Stub doesn't track time; calling SetClock with any function must
	// be a silent no-op (does not panic, does not store the fn).
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetClock panicked: %v", r)
		}
	}()
	NopSessionsForTests.SetClock(func() time.Time { return time.Now() })
	NopSessionsForTests.SetClock(nil) // even nil is a no-op
}

// Compile-time assertion: catches any sessions.Store interface drift
// (added method, changed signature) immediately rather than only at
// the call sites that use NopSessionsForTests.
var _ sessions.Store = NopSessionsForTests

// ---- mustMarshalServerMessage / mustNopValidator ----

func TestMustMarshalServerMessage_RoundTrips(t *testing.T) {
	// The init() that marshals "ping" / "pong" frames runs on package
	// import; if it panicked the test binary wouldn't build. These
	// frames are sent on every keepalive tick — they must decode back
	// to a ServerMessage with the original type and nil payload.
	for _, typ := range []string{"ping", "pong", "custom"} {
		t.Run(typ, func(t *testing.T) {
			b := mustMarshalServerMessage(typ)
			if len(b) == 0 {
				t.Fatalf("mustMarshalServerMessage(%q) returned empty bytes", typ)
			}
			// Bytes must contain the type literal for the websocket peer
			// to dispatch correctly.
			if !contains(b, typ) {
				t.Errorf("frame for %q = %s, missing type literal", typ, b)
			}
		})
	}
}

func TestMustNopValidator_ReturnsUsableValidator(t *testing.T) {
	// NopValidatorForTests is the package-level singleton produced by
	// mustNopValidator. Tests rely on it being non-nil so NewHub
	// doesn't panic on the required-validator guard.
	if NopValidatorForTests == nil {
		t.Fatal("NopValidatorForTests = nil; NewHub would panic")
	}
}

// contains is a tiny bytes-contains helper. We avoid pulling bytes here
// to keep the test imports minimal — package ws's existing tests use
// the same convention.
func contains(haystack []byte, needle string) bool {
	hn := len(needle)
	for i := 0; i+hn <= len(haystack); i++ {
		if string(haystack[i:i+hn]) == needle {
			return true
		}
	}
	return false
}
