//go:build linux

package restricteddispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestScopedRunnersDoNotAcknowledgeFailedDurableWrites(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, stage := range []string{"user", "assistant", "completion"} {
			t.Run(map[bool]string{false: "text", true: "native"}[native]+"/"+stage, func(t *testing.T) {
				session := &nativeBoundarySession{output: "{\"type\":\"text\",\"text\":\"answer\"}\n{\"type\":\"done\"}\n", done: make(chan struct{})}
				close(session.done)
				starts := 0
				a, run := boundaryRunner(t, native, func(context.Context, string) (TextSession, error) { starts++; return session, nil })
				trigger := `CREATE TRIGGER fail_context_write BEFORE INSERT ON access_context WHEN NEW.role='` + stage + `' BEGIN SELECT RAISE(ABORT,'fixture durable write failed'); END`
				if stage == "completion" {
					trigger = `CREATE TRIGGER fail_completion BEFORE UPDATE OF completed_at ON access_attempts WHEN NEW.completed_at IS NOT NULL AND OLD.completed_at IS NULL BEGIN SELECT RAISE(ABORT,'fixture durable write failed'); END`
				}
				if _, err := a.Store.DB.ExecContext(t.Context(), trigger); err != nil {
					t.Fatal(err)
				}
				done := false
				err := run(t.Context(), "h1", "w", "c1", "input", func(kind, text string) error { done = done || kind == "done"; return nil })
				if err == nil || !strings.Contains(err.Error(), "fixture durable write failed") || done {
					t.Fatalf("storage failure acknowledged: %v done=%v", err, done)
				}
				want := 1
				if stage == "user" {
					want = 0
				}
				if starts != want || session.stops != want {
					t.Fatalf("worker lifecycle starts=%d stops=%d want=%d", starts, session.stops, want)
				}
				var live int
				if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM access_attempts WHERE revoked_at IS NULL AND completed_at IS NULL`).Scan(&live); err != nil || live != 0 {
					t.Fatalf("failed execution retained authority: %d %v", live, err)
				}
			})
		}
	}
}

func TestProviderAndPromptStorageFailuresCannotCreateLaunchPlans(t *testing.T) {
	a := nativeFixture(t)
	_, attempt := prepareNativeFixture(t, a, "h1", "c1")
	if err := a.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"native plan", func() error { return a.attachNative(t.Context(), attempt, &restrictedruntime.Plan{}) }},
		{"provider plan", func() error { return a.attachProvider(t.Context(), attempt, &restrictedruntime.Plan{}) }},
		{"provider snapshot", func() error { _, err := providerCurrent(t.Context(), a.Store.DB, attempt, ""); return err }},
		{"provider validation", func() error { _, err := a.checkedProvider(t.Context(), attempt, providerBinding{}); return err }},
		{"provider pin", func() error { return a.pinProvider(t.Context(), attempt, 128) }},
		{"delegated provider pin", func() error {
			child := attempt
			child.Parent = "parent"
			return a.pinDelegatedProvider(t.Context(), child, 128, "native_api_key")
		}},
		{"native prompt", func() error {
			_, err := a.freezeNativePrompt(t.Context(), attempt, NativePrompt{Input: "input"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("closed storage accepted")
			}
		})
	}
	start := func(context.Context, string) (TextSession, error) {
		t.Fatal("started with closed storage")
		return nil, nil
	}
	for _, run := range []boundaryExecute{(&NativeRunner{Authority: a, StartSession: start}).Execute, (&TextRunner{Authority: a, StartSession: start}).Execute} {
		if err := run(t.Context(), "h1", "w", "c1", "input", func(string, string) error { return nil }); err == nil {
			t.Fatal("unavailable chat audience accepted")
		}
	}
}

func TestNativePlanRejectsRuntimePolicyMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*restrictedruntime.Plan)
	}{
		{"wrong executable", func(p *restrictedruntime.Plan) { p.Command[0] = "/bin/sh" }},
		{"missing network", func(p *restrictedruntime.Plan) { p.Network = nil }},
		{"missing responses policy", func(p *restrictedruntime.Plan) { p.Network.Grants[0].Responses = nil }},
		{"wrong model", func(p *restrictedruntime.Plan) { p.Command[2] = "gpt-foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := nativeFixture(t)
			handle, attempt := prepareNativeFixture(t, a, "h1", "c1")
			plan, err := a.Resolve(t.Context(), handle)
			if err != nil {
				t.Fatal(err)
			}
			// Reconstruct the provider stage before native conversion, then tamper with it.
			if err := a.attachProvider(t.Context(), attempt, &plan); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&plan)
			if err := a.attachNative(t.Context(), attempt, &plan); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("mismatched runtime policy accepted: %v", err)
			}
		})
	}
}

func TestScopedRunnersRejectWrongExecutionProfile(t *testing.T) {
	for _, native := range []bool{false, true} {
		a, run := boundaryRunner(t, native, func(context.Context, string) (TextSession, error) { t.Fatal("wrong profile launched"); return nil, nil })
		if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='disabled' WHERE id='a'`); err != nil {
			t.Fatal(err)
		}
		if err := run(t.Context(), "h1", "w", "c1", "input", func(string, string) error { return nil }); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("disabled profile accepted: %v", err)
		}
	}
}
