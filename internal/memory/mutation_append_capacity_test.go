package memory

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory/memdiff"
)

func TestMutationAppendCapacityRejectsOverflow(t *testing.T) {
	const maxInt = int(^uint(0) >> 1)
	for _, tc := range []struct {
		name                string
		base, content, want int
		overflow            bool
	}{
		{"empty", 0, 0, 0, false},
		{"normal", 12, 7, 19, false},
		{"largest base", maxInt, 0, maxInt, false},
		{"largest content", 0, maxInt, maxInt, false},
		{"exact boundary", maxInt - 1, 1, maxInt, false},
		{"one past boundary", maxInt, 1, 0, true},
		{"reversed operands", 1, maxInt, 0, true},
		{"wrapped negative", maxInt, maxInt, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mutationAppendCapacity(tc.base, tc.content)
			if errors.Is(err, ErrContentTooLarge) != tc.overflow || got != tc.want {
				t.Fatalf("capacity(%d, %d) = %d, %v; want %d, overflow=%v", tc.base, tc.content, got, err, tc.want, tc.overflow)
			}
		})
	}
}

func TestBuildTargetAppendUsesBothCapacityPaths(t *testing.T) {
	for _, declared := range []bool{false, true} {
		r := MutateRequest{Op: OpAppend}
		if declared {
			r.Removals = []memdiff.Removal{}
		}
		got, normalized, verified, err := r.buildTarget([]byte("base\n"), []byte("content\n"))
		if err != nil || string(got) != "base\ncontent\n" || normalized || verified != declared {
			t.Fatalf("declared=%v: got %q, normalized=%v, verified=%v, err=%v", declared, got, normalized, verified, err)
		}
	}
}
