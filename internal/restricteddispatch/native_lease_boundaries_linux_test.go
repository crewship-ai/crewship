//go:build linux

package restricteddispatch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestNativeCompletionRejectsInvalidIssuedHistoryWithoutConsumingLease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issued []json.RawMessage
	}{
		{"missing output", nil},
		{"malformed output", []json.RawMessage{json.RawMessage(`{`)}},
		{"duplicate ids", []json.RawMessage{json.RawMessage(`{"type":"message","id":"same"}`), json.RawMessage(`{"type":"message","id":"same"}`)}},
		{"missing call id", []json.RawMessage{json.RawMessage(`{"type":"function_call","id":"call"}`)}},
		{"duplicate calls", []json.RawMessage{json.RawMessage(`{"type":"function_call","id":"first","call_id":"same"}`), json.RawMessage(`{"type":"function_call","id":"second","call_id":"same"}`)}},
		{"history too large", []json.RawMessage{json.RawMessage(`{"type":"message","id":"message","text":"` + strings.Repeat("x", 1<<20) + `"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := nativeFixture(t)
			handle, attempt := prepareNativeFixture(t, a, "h1", "c1")
			_, ticket, err := a.NativeRequest(t.Context(), handle, "key", nativeRaw(t))
			if err != nil {
				t.Fatal(err)
			}
			if err = a.NativeComplete(t.Context(), handle, ticket, tc.issued); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("invalid completion accepted: %v", err)
			}
			var lease, history string
			if err = a.Store.DB.QueryRowContext(t.Context(), `SELECT in_flight_lease,history_json FROM restricted_native_sessions WHERE attempt_id=?`, attempt.ID).Scan(&lease, &history); err != nil || lease != ticket || history != "[]" {
				t.Fatalf("refused completion changed durable state: lease=%q history bytes=%d err=%v", lease, len(history), err)
			}
			if err = a.NativeComplete(t.Context(), handle, ticket, []json.RawMessage{json.RawMessage(`{"type":"message","id":"valid"}`)}); err != nil {
				t.Fatalf("valid retry refused: %v", err)
			}
		})
	}
}

func TestNativeRequestAndCompletionWriteFailuresRollbackTheirLease(t *testing.T) {
	for _, stage := range []string{"request", "completion"} {
		t.Run(stage, func(t *testing.T) {
			a := nativeFixture(t)
			handle, attempt := prepareNativeFixture(t, a, "h1", "c1")
			ticket := ""
			if stage == "completion" {
				var err error
				_, ticket, err = a.NativeRequest(t.Context(), handle, "key", nativeRaw(t))
				if err != nil {
					t.Fatal(err)
				}
			}
			var before string
			if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT json_array(client_prefix_json,history_json,in_flight_lease) FROM restricted_native_sessions WHERE attempt_id=?`, attempt.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Store.DB.ExecContext(t.Context(), `CREATE TRIGGER fixture_reject BEFORE UPDATE ON restricted_native_sessions BEGIN SELECT RAISE(ABORT,'fixture lease failure'); END`); err != nil {
				t.Fatal(err)
			}
			if stage == "request" {
				if body, next, err := a.NativeRequest(t.Context(), handle, "key", nativeRaw(t)); err == nil || body != nil || next != "" {
					t.Fatalf("failed lease acquired: %v", err)
				}
			} else {
				if err := a.NativeComplete(t.Context(), handle, ticket, []json.RawMessage{json.RawMessage(`{"type":"message","id":"valid"}`)}); err == nil {
					t.Fatal("failed completion reported success")
				}
			}
			var after string
			if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT json_array(client_prefix_json,history_json,in_flight_lease) FROM restricted_native_sessions WHERE attempt_id=?`, attempt.ID).Scan(&after); err != nil || after != before {
				t.Fatalf("lease write partially committed: %v", err)
			}
			if _, err := a.Store.DB.ExecContext(t.Context(), `DROP TRIGGER fixture_reject`); err != nil {
				t.Fatal(err)
			}
			if stage == "request" {
				var err error
				_, ticket, err = a.NativeRequest(t.Context(), handle, "key", nativeRaw(t))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := a.NativeComplete(t.Context(), handle, ticket, []json.RawMessage{json.RawMessage(`{"type":"message","id":"valid"}`)}); err != nil {
				t.Fatalf("lease retry failed: %v", err)
			}
		})
	}
}

func TestNativeCorruptConversationAndForeignCredentialFailClosed(t *testing.T) {
	a := nativeFixture(t)
	handle, attempt := prepareNativeFixture(t, a, "h1", "c1")
	if body, ticket, err := a.NativeRequest(t.Context(), handle, "foreign", nativeRaw(t)); body != nil || ticket != "" || !errors.Is(err, access.ErrDenied) {
		t.Fatalf("foreign key accepted: %v", err)
	}
	if err := a.NativeComplete(t.Context(), handle, "", []json.RawMessage{json.RawMessage(`{}`)}); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("empty lease accepted: %v", err)
	}
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE restricted_native_sessions SET history_json='{' WHERE attempt_id=?`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if body, ticket, err := a.NativeRequest(t.Context(), handle, "key", nativeRaw(t)); body != nil || ticket != "" || !errors.Is(err, access.ErrDenied) {
		t.Fatalf("corrupt history dispatched: %v", err)
	}
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE restricted_native_sessions SET history_json='[]' WHERE attempt_id=?`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	_, ticket, err := a.NativeRequest(t.Context(), handle, "key", nativeRaw(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.ExecContext(t.Context(), `UPDATE restricted_native_sessions SET history_json='{' WHERE attempt_id=?`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.NativeComplete(t.Context(), handle, ticket, []json.RawMessage{json.RawMessage(`{"type":"message","id":"valid"}`)}); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("corrupt completion repaired itself silently: %v", err)
	}
}
