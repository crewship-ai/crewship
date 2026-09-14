package memdiff

import (
	"strings"
	"testing"
)

// TestApplyRejectsMalformedScripts covers the guards on Apply. Apply is
// exported and the property tests feed it only well-formed scripts, so these
// are the cases that prove a hand-built or transport-corrupted script cannot
// panic the server.
func TestApplyRejectsMalformedScripts(t *testing.T) {
	base := lines(t, "a\nb\n")

	tests := []struct {
		name string
		ops  []Op
		want string
	}{
		{
			name: "equal span past the end",
			ops:  []Op{{Kind: OpEqual, StartLine: 2, LineCount: 5, Lines: base}},
			want: "outside a 2-line base",
		},
		{
			name: "delete span starting at 0",
			ops:  []Op{{Kind: OpDelete, StartLine: 0, LineCount: 1}},
			want: "outside a 2-line base",
		},
		{
			name: "zero-length equal",
			ops:  []Op{{Kind: OpEqual, StartLine: 1, LineCount: 0}},
			want: "outside a 2-line base",
		},
		{
			name: "insert anchored past the end",
			ops:  []Op{{Kind: OpInsert, StartLine: 9, LineCount: 1, Lines: lines(t, "x\n")}},
			want: "anchored at base line 9",
		},
		{
			name: "insert anchored before line 0",
			ops:  []Op{{Kind: OpInsert, StartLine: -1, LineCount: 1, Lines: lines(t, "x\n")}},
			want: "anchored at base line -1",
		},
		{
			name: "insert whose LineCount lies about its payload",
			ops:  []Op{{Kind: OpInsert, StartLine: 0, LineCount: 3, Lines: lines(t, "x\n")}},
			want: "declares 3 lines but carries 1",
		},
		{
			name: "unknown op kind",
			ops:  []Op{{Kind: OpKind(42), StartLine: 1, LineCount: 1}},
			want: "unknown kind 42",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Apply(base, tc.ops)
			if err == nil {
				t.Fatalf("Apply accepted a malformed script and returned %q", asStrings(got))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestApplyEmptyScript(t *testing.T) {
	got, err := Apply(nil, nil)
	if err != nil {
		t.Fatalf("Apply(nil,nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Apply(nil,nil) = %q, want no lines", asStrings(got))
	}
}

func TestOpKindString(t *testing.T) {
	for kind, want := range map[OpKind]string{
		OpEqual:    "equal",
		OpDelete:   "delete",
		OpInsert:   "insert",
		OpKind(99): "OpKind(99)",
		OpKind(-1): "OpKind(-1)",
	} {
		if got := kind.String(); got != want {
			t.Errorf("OpKind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}
