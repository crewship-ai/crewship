package api

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Allocates only through take: the production maps are lazy. A full retained map
// includes idle entries awaiting the next capacity sweep, the memory worst case.
func BenchmarkSharedSearchRetainedMemory(b *testing.B) {
	for _, accounts := range []int{1, 64, 4096} {
		b.Run(fmt.Sprintf("accounts%d", accounts), func(b *testing.B) {
			for iteration := 0; iteration < b.N; iteration++ {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				s := newSharedSearchLimiter()
				now := time.Now()
				s.now = func() time.Time { return now }
				for a := 0; a < accounts; a++ {
					for p := 0; p < 257; p++ {
						project := fmt.Sprintf("project%d", p)
						if p == 256 {
							project = ""
						}
						if ok, _ := s.take(context.Background(), fmt.Sprintf("user%d", a), "ws", project); !ok {
							b.Fatal("unexpected capacity refusal")
						}
						now = now.Add(3 * time.Second)
					}
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)), "retained-bytes")
				b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/float64(accounts*257), "bytes/audience")
				if len(s.accounts) != accounts {
					b.Fatalf("accounts%d want%d", len(s.accounts), accounts)
				}
				runtime.KeepAlive(s)
			}
		})
	}
}
