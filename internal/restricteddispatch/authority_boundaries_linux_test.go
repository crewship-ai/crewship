//go:build linux

package restricteddispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestCommandPreparationRejectsInvalidAndOversizedArguments(t *testing.T) {
	a := fixture(t)
	many := make([]string, 65)
	for i := range many {
		many[i] = "arg"
	}
	for _, args := range [][]string{nil, {""}, many, {"/bin/true", "bad\x00argument"}, {"/bin/true", strings.Repeat("x", 65537)}, {"/bin/true", strings.Repeat("\n", 40000), strings.Repeat("\n", 40000)}} {
		if handle, _, err := a.Prepare(t.Context(), "h1", "w", "a", "c1", "", nil, command(args...)); handle != "" || !errors.Is(err, access.ErrDenied) {
			t.Fatalf("invalid command persisted: %v", err)
		}
	}
	if _, _, err := a.Prepare(t.Context(), "h1", "w", "a", "c1", "", nil, nil); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("nil builder accepted: %v", err)
	}
	var launches, live int
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_launches`).Scan(&launches); err != nil || launches != 0 {
		t.Fatalf("invalid launch persisted: %d %v", launches, err)
	}
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM access_attempts WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("invalid command retained authority: %d %v", live, err)
	}
	if path, err := a.Volume(t.Context(), restrictedruntime.Plan{}, restrictedruntime.Mount{}); path != "" || !errors.Is(err, access.ErrDenied) {
		t.Fatalf("implicit persistent mount granted: %q %v", path, err)
	}
}

func TestCommandPersistenceFailureRevokesAndAllowsNewAdmission(t *testing.T) {
	a := fixture(t)
	if _, err := a.Store.DB.ExecContext(t.Context(), `CREATE TRIGGER fixture_reject BEFORE INSERT ON restricted_launches BEGIN SELECT RAISE(ABORT,'fixture launch failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Prepare(t.Context(), "h1", "w", "a", "c1", "", nil, command("/bin/true")); err == nil || !strings.Contains(err.Error(), "persist isolated command") {
		t.Fatalf("failed launch persistence accepted: %v", err)
	}
	var live int
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM access_attempts WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("failed persistence retained authority: %d %v", live, err)
	}
	if _, err := a.Store.DB.ExecContext(t.Context(), `DROP TRIGGER fixture_reject`); err != nil {
		t.Fatal(err)
	}
	handle, _, err := a.Prepare(t.Context(), "h1", "w", "a", "c1", "", nil, command("/bin/true"))
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := a.Resolve(t.Context(), handle); err != nil || len(plan.Command) != 1 || plan.Command[0] != "/bin/true" {
		t.Fatalf("retry failed: %+v %v", plan, err)
	}
}

func TestCorruptStoredLaunchCannotBecomeRuntimePlan(t *testing.T) {
	a := fixture(t)
	for _, raw := range []string{"{", "[]", `[""]`, `["/bin/true","bad\u0000argument"]`, strings.Repeat("x", 131073)} {
		handle, attempt, err := a.Store.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Store.DB.ExecContext(t.Context(), `INSERT INTO restricted_launches(attempt_id,command_json) VALUES(?,?)`, attempt.ID, raw)
		// Malformed/oversized envelopes are refused by the SQL contract;
		// syntactically valid but unsafe argv is refused by Resolve.
		if raw == "{" || len(raw) > 131072 {
			if err == nil {
				t.Fatal("storage accepted an invalid command envelope")
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if plan, err := a.Resolve(t.Context(), handle); err == nil || len(plan.Command) != 0 {
			t.Fatalf("corrupt command became runtime plan: %+v %v", plan, err)
		}
	}
}

func TestNativePromptLimitsAndWriteFailureRevokeAttempt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prompt  NativePrompt
		failure bool
	}{
		{name: "empty input"},
		{name: "invalid input UTF8", prompt: NativePrompt{Input: string([]byte{0xff})}},
		{name: "invalid instructions UTF8", prompt: NativePrompt{Input: "input", Instructions: string([]byte{0xff})}},
		{name: "oversized instructions", prompt: NativePrompt{Input: "input", Instructions: strings.Repeat("x", (128<<10)+1)}},
		{name: "oversized input", prompt: NativePrompt{Input: strings.Repeat("x", (512<<10)+1)}},
		{name: "prompt storage failure", prompt: NativePrompt{Input: "input"}, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := nativeFixture(t)
			if tc.failure {
				if _, err := a.Store.DB.ExecContext(t.Context(), `CREATE TRIGGER fixture_reject BEFORE INSERT ON restricted_native_sessions BEGIN SELECT RAISE(ABORT,'fixture prompt failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if handle, _, err := a.PrepareNative(t.Context(), "h1", "w", "a", "c1", "", nil, 128, func(context.Context, access.Attempt) (NativePrompt, error) { return tc.prompt, nil }); handle != "" || err == nil {
				t.Fatalf("invalid native prompt accepted: %v", err)
			}
			var live int
			if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM access_attempts WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
				t.Fatalf("invalid prompt retained authority: %d %v", live, err)
			}
		})
	}
}

func TestNativePreparationRejectsMissingBuilderLimitsAndBuildErrors(t *testing.T) {
	a := nativeFixture(t)
	if _, _, err := a.PrepareNative(t.Context(), "h1", "w", "a", "c1", "", nil, 128, nil); !errors.Is(err, access.ErrDenied) {
		t.Fatal(err)
	}
	build := func(context.Context, access.Attempt) (NativePrompt, error) {
		t.Fatal("builder called beyond output budget")
		return NativePrompt{}, nil
	}
	for _, limit := range []int64{0, 32769} {
		if _, _, err := a.PrepareNative(t.Context(), "h1", "w", "a", "c1", "", nil, limit, build); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("invalid limit accepted: %v", err)
		}
	}
	failure := errors.New("fixture prompt unavailable")
	if _, _, err := a.PrepareNative(t.Context(), "h1", "w", "a", "c1", "", nil, 128, func(context.Context, access.Attempt) (NativePrompt, error) { return NativePrompt{}, failure }); !errors.Is(err, failure) {
		t.Fatalf("prompt failure hidden: %v", err)
	}
}

func TestRunProofEncryptedEnvelopeRefusesIncompleteOrCorruptClaims(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("31", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	for _, raw := range []string{"{", `{}`, `{"handle":"host-only","sources":[]}`, `{"handle":"","sources":["origin"]}`, strings.Repeat("x", 32769)} {
		cipher, err := encryption.Encrypt(raw)
		if err != nil {
			t.Fatal(err)
		}
		if proof, err := OpenRunProof(cipher); !errors.Is(err, access.ErrDenied) || len(proof.ContextIDs()) != 0 {
			t.Fatalf("invalid envelope accepted: %v", err)
		}
	}
	ids := make([]string, 65)
	for i := range ids {
		ids[i] = "origin"
	}
	raw, err := json.Marshal(map[string]any{"handle": "host-only", "sources": ids})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := encryption.Encrypt(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenRunProof(cipher); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("too many sources accepted: %v", err)
	}
	if _, err = NewRunProof("host-only", ids).Seal(); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("too many sources sealed: %v", err)
	}
}
