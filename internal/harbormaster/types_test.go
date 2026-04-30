package harbormaster

import "testing"

// TestMode_String pins the human-readable rendering used in logs and
// journal entries. Adding a new Mode constant must consciously update
// this test.
func TestMode_String(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeNone, "none"},
		{ModeAsync, "async"},
		{ModeSync, "sync"},
		{Mode(99), "none"}, // unknown falls through to default
		{Mode(-1), "none"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("Mode(%d).String() = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}
