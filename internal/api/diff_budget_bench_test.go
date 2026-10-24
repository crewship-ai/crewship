package api

import (
	"fmt"
	"strings"
	"testing"
)

// Accepted-budget matcher work, NOT HTTP latency or production traffic. The
// actual valid Save->version->Diff endpoint has separate regression tests.
func BenchmarkDiffBudgetAccepted(b *testing.B) {
	var unique, reverse, periodic, shifted strings.Builder
	for i := 0; i < 1998; i++ {
		fmt.Fprintf(&unique, "owned-%04d\n", i)
		fmt.Fprintf(&reverse, "owned-%04d\n", 1997-i)
		fmt.Fprintf(&periodic, "owned-%03d\n", i%128)
		fmt.Fprintf(&shifted, "owned-%03d\n", (i+63)%128)
	}
	longA := strings.Repeat("a", (128<<10)-1)
	longB := strings.Repeat("b", (128<<10)-1)
	for _, tc := range []struct{ name, a, c string }{
		{"unique-reverse", unique.String(), reverse.String()},
		{"periodic-shift", periodic.String(), shifted.String()},
		{"no-common", strings.Repeat("old\n", 1998), strings.Repeat("new\n", 1998)},
		{"large-single-lines", longA, longB},
	} {
		b.Run(tc.name, func(b *testing.B) {
			budget := testDiffBudget(256<<10, 2000)
			if err := budget.checkText(tc.a, tc.c); err != nil {
				b.Fatal("invalid accepted fixture", err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.a) + len(tc.c)))
			b.ResetTimer()
			for range b.N {
				release, ok := budget.acquire()
				if !ok {
					b.Fatal("unexpected capacity failure")
				}
				out, err := budget.render(tc.a, tc.c, "a", "b")
				release()
				if err != nil || len(out) == 0 {
					b.Fatalf("diff fixture failed: %v", err)
				}
			}
		})
	}
}
