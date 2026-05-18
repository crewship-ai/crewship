package license

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// enforce.go — v0.1 no-op limit checks + LimitError shape.
//
// All three Check*Limit methods are explicitly documented as no-ops
// in v0.1 (kept as a kostra/skeleton so v0.2 can re-enable tiered
// limits without re-introducing types or call sites). These tests pin
// the no-op contract so a regression that started returning errors
// here would break agents_create.go + workspaces_membership.go's
// happy paths before v0.2 is ready.
//
// LimitError and IsLimitError are referenced by the 402 handlers in
// those call sites; they need to keep their exact shape so the
// handlers don't have to change when v0.2 starts producing them.
// ---------------------------------------------------------------------------

func TestCheckCrewLimit_NoOp(t *testing.T) {
	// v0.1 contract: returns nil for any inputs. DB arg can even be
	// nil — the function never reads it.
	lic := New()
	cases := []struct{ name, workspaceID string }{
		{"non-empty-workspace", "ws-1"},
		{"empty-workspace", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := lic.CheckCrewLimit(context.Background(), nil, tc.workspaceID); err != nil {
				t.Errorf("CheckCrewLimit(%q) = %v, want nil (v0.1 no-op)", tc.workspaceID, err)
			}
		})
	}
}

func TestCheckAgentLimit_NoOp(t *testing.T) {
	lic := New()
	if err := lic.CheckAgentLimit(context.Background(), nil, "ws-1"); err != nil {
		t.Errorf("CheckAgentLimit = %v, want nil", err)
	}
	if err := lic.CheckAgentLimit(context.Background(), nil, ""); err != nil {
		t.Errorf("CheckAgentLimit(empty) = %v, want nil", err)
	}
}

func TestCheckMemberLimit_NoOp(t *testing.T) {
	lic := New()
	if err := lic.CheckMemberLimit(context.Background(), nil, "ws-1"); err != nil {
		t.Errorf("CheckMemberLimit = %v, want nil", err)
	}
	if err := lic.CheckMemberLimit(context.Background(), nil, ""); err != nil {
		t.Errorf("CheckMemberLimit(empty) = %v, want nil", err)
	}
}

func TestCheckLimits_OnNilLicense(t *testing.T) {
	// The handlers occasionally call these with a nil-License sentinel
	// when no license was loaded — pin that the method calls don't
	// panic. (Methods on nil pointer receivers work fine in Go since
	// the body never derefs the receiver.)
	var lic *License // nil
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil-License Check*Limit panicked: %v", r)
		}
	}()
	if err := lic.CheckCrewLimit(context.Background(), nil, "ws"); err != nil {
		t.Errorf("nil CheckCrewLimit = %v, want nil", err)
	}
	if err := lic.CheckAgentLimit(context.Background(), nil, "ws"); err != nil {
		t.Errorf("nil CheckAgentLimit = %v, want nil", err)
	}
	if err := lic.CheckMemberLimit(context.Background(), nil, "ws"); err != nil {
		t.Errorf("nil CheckMemberLimit = %v, want nil", err)
	}
}

// ---- LimitError ----

func TestLimitError_ErrorMessage(t *testing.T) {
	// v0.1 never constructs one, but downstream 402 handlers will read
	// the message verbatim when v0.2 lands. Pin the format so any
	// refactor that changes the wording surfaces here before changing
	// the user-facing 402 body.
	e := &LimitError{
		Resource: "crews",
		Current:  3,
		Maximum:  2,
		Edition:  Edition("free"),
	}
	got := e.Error()
	for _, fragment := range []string{"license limit reached", "3/2", "crews", "free"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("Error() = %q, missing fragment %q", got, fragment)
		}
	}
}

func TestLimitError_ImplementsErrorInterface(t *testing.T) {
	// Compile-time assertion that *LimitError satisfies the error
	// interface — would catch a refactor that broke the Error()
	// receiver signature.
	var _ error = (*LimitError)(nil)
}

// ---- IsLimitError ----

func TestIsLimitError_DetectsLimitError(t *testing.T) {
	e := &LimitError{Resource: "agents", Current: 1, Maximum: 1, Edition: Edition("free")}
	if !IsLimitError(e) {
		t.Error("IsLimitError(*LimitError) = false, want true")
	}
	// Errors-as wrapping is NOT supported by the current implementation
	// — IsLimitError does a direct type-assertion, not errors.As. Pin
	// the current behavior so a future migration to errors.As
	// (which the wrapped-error idiom would prefer) is an explicit
	// breaking-change visible here. Uses fmt.Errorf with %w so the
	// assertion actually exercises an error chain — a plain errors.New
	// concatenation wouldn't form a wrap and the test would silently
	// pass regardless of whether IsLimitError used errors.As or not.
	wrapped := fmt.Errorf("wrapped: %w", e)
	if IsLimitError(wrapped) {
		t.Error("IsLimitError(wrapped) = true; current implementation requires direct *LimitError, not errors.As")
	}
}

func TestIsLimitError_RejectsOther(t *testing.T) {
	if IsLimitError(nil) {
		t.Error("IsLimitError(nil) = true, want false")
	}
	if IsLimitError(errors.New("some other error")) {
		t.Error("IsLimitError(plain error) = true, want false")
	}
	// errors.Is-style sentinel — must NOT match (it's not a *LimitError).
	type customErr struct{ msg string }
	// inline struct without Error() method won't satisfy error; use the
	// inline declaration below.
	if IsLimitError(&fakeNonLimit{}); IsLimitError(&fakeNonLimit{}) {
		t.Error("IsLimitError(*fakeNonLimit) = true; only *LimitError should match")
	}
	_ = customErr{} // silence unused
}

type fakeNonLimit struct{}

func (*fakeNonLimit) Error() string { return "not a limit error" }
