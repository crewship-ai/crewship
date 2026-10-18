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
					for p := 0; p < min(257, sharedSearchTotalAudiences/accounts); p++ {
						project := fmt.Sprintf("project%d", p)
						if p == 256 {
							project = ""
						}
						if ok, _ := s.take(context.Background(), fmt.Sprintf("user%d", a), "ws", project); !ok {
							b.Fatal("unexpected capacity refusal")
						}
						if accounts < 4096 {
							now = now.Add(3 * time.Second)
						}
					}
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)), "retained-bytes")
				b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/float64(s.total), "bytes/audience")
				if len(s.accounts) != accounts {
					b.Fatalf("accounts%d want%d", len(s.accounts), accounts)
				}
				runtime.KeepAlive(s)
			}
		})
	}
}

// Grow 120 maps to257 audiences, expire all but one, then fill the global cap.
// This exercises retained backing storage after churn through production take.
func BenchmarkSharedSearchRetainedMemoryAfterChurn(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		s := newSharedSearchLimiter()
		now := time.Now()
		s.now = func() time.Time { return now }
		take := func(user, project int) {
			audience := fmt.Sprintf("p%d", project)
			if project == 256 {
				audience = ""
			}
			if ok, _ := s.take(context.Background(), fmt.Sprintf("u%d", user), "ws", audience); !ok {
				b.Fatal("unexpected capacity refusal")
			}
		}
		for project := 0; project < 257; project++ {
			for user := 0; user < 120; user++ {
				take(user, project)
			}
			now = now.Add(3 * time.Second)
		}
		now = now.Add(4 * time.Minute)
		for user := 0; user < 120; user++ {
			take(user, 0)
		}
		now = now.Add(time.Minute)
		for user := 120; user < 4096; user++ {
			for project := 0; project < 8; project++ {
				take(user, project)
			}
		}
		for user := 120; s.total < sharedSearchTotalAudiences; user++ {
			take(user, 8)
		}
		if s.total != sharedSearchTotalAudiences || len(s.accounts) != 4096 {
			b.Fatal("churn did not fill exact global capacity")
		}
		for user := 0; user < 120; user++ {
			if len(s.accounts[sharedSearchIdentity(fmt.Sprintf("u%d", user), "ws")].audiences) != 1 {
				b.Fatal("expired high-capacity map not reclaimed")
			}
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)), "retained-bytes")
		b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/float64(s.total), "bytes/audience")
		runtime.KeepAlive(s)
	}
}
