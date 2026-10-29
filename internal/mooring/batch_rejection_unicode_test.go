package mooring

import (
	"strings"
	"testing"
)

// JSON Schema maxLength and Rust char counts use Unicode scalar values, not
// UTF-8 bytes. A valid engine rejection must not poison the RPC channel.
func TestBatchRejectionUnicodeScalarLimit(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		valid         bool
	}{
		{"1000 astral scalars", strings.Repeat("😀", 1000), true},
		{"1001 astral scalars", strings.Repeat("😀", 1001), false},
		{"1000 ASCII", strings.Repeat("x", 1000), true},
		{"1001 ASCII", strings.Repeat("x", 1001), false},
		{"invalid UTF8", string([]byte{0xff}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			direct := &BatchRejection{Code: "invalid_request", Message: tc.message, Retry: "do_not_retry"}
			if got := validBatchRejection(direct); got != tc.valid {
				t.Fatalf("direct rejection accepted=%v want=%v", got, tc.valid)
			}
			dependent := &BatchRejection{Code: "dependency_rejected", Message: "dependency failed", Retry: "do_not_retry", RootOperationID: "root", RootCause: &BatchRootCause{OperationID: "root", Code: "invalid_request", Message: tc.message}}
			if got := validBatchRejection(dependent); got != tc.valid {
				t.Fatalf("root cause accepted=%v want=%v", got, tc.valid)
			}
		})
	}
}
