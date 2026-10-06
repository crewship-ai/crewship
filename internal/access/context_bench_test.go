package access

import (
	"errors"
	"fmt"
	"testing"
)

// BenchmarkCompletedConversationProvenance reproduces the incremental workload
// in TestLongCompletedConversationRetainsBoundedPromptAndRevocation. The 128
// and 155 turn cases reach and exceed the 256-entry context window, respectively.
// Every iteration starts with a private database; fixture migration/copying is
// excluded from the timer. Authority checks and revocation remain in the workload.
//
// For a bounded scaling profile:
//
//	go test ./internal/access -run '^$' -bench '^BenchmarkCompletedConversationProvenance$' -benchtime=1x -benchmem -cpuprofile /tmp/access.pprof -o /tmp/access.test
func BenchmarkCompletedConversationProvenance(b *testing.B) {
	for _, turns := range []int{16, 64, 128, 155} {
		b.Run(fmt.Sprintf("turns_%d", turns), func(b *testing.B) {
			b.ReportAllocs()
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				s := fixture(b)
				policy(b, s, "h1", Right{"agent", "a", "run"})
				b.StartTimer()
				var first, last string
				for turn := 0; turn < turns; turn++ {
					h, a, err := s.Admit(b.Context(), "h1", "w", "a", "c1", "", nil)
					if err != nil {
						b.Fatal(err)
					}
					prompt, err := s.BuildContext(b.Context(), a, "next")
					if err != nil {
						b.Fatalf("turn %d: %v", turn, err)
					}
					if len(prompt.Input) > 96000 {
						b.Fatalf("unbounded prompt at turn %d", turn)
					}
					text := "tiny completed turn"
					if turn == 0 {
						text = "LONG_CONVERSATION_ORIGIN_CANARY"
						first = h
					}
					if _, err = s.AppendContext(b.Context(), h, ContextUser, "tiny user"); err != nil {
						b.Fatal(err)
					}
					if _, err = s.AppendContext(b.Context(), h, ContextAssistant, text); err != nil {
						b.Fatal(err)
					}
					if err = s.CompleteAttempt(b.Context(), h); err != nil {
						b.Fatal(err)
					}
					last = h
				}
				var direct int
				if err := s.DB.QueryRow(`SELECT COUNT(*) FROM access_context_dependencies WHERE attempt_id=(SELECT id FROM access_attempts WHERE handle_hash=?)`, digest(last)).Scan(&direct); err != nil || direct > 256 {
					b.Fatalf("unbounded direct provenance %d: %v", direct, err)
				}
				if err := s.CheckContextAttempt(b.Context(), last); err != nil {
					b.Fatalf("completed provenance: %v", err)
				}
				if err := s.RevokeAttempt(b.Context(), first); err != nil {
					b.Fatal(err)
				}
				if err := s.CheckContextAttempt(b.Context(), last); !errors.Is(err, ErrDenied) {
					b.Fatalf("old causal origin failed to revoke latest: %v", err)
				}
				b.StopTimer()
				if err := s.DB.Close(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(turns), "turns/op")
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*turns), "ns/turn")
		})
	}
}
