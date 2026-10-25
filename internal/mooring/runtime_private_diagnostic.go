package mooring

// PRIVATE DIAGNOSTIC OVERLAY ONLY. This file must never enter a release.
// The sink retains finite enum ordinals, not stderr lines or unknown bytes.
import "sync"

const privateDiagnosticRetained = 16
const privateDiagnosticLineLimit = 64

var privateDiagnosticLines = [...]string{
	"mooring-diag-v1:owner_check",
	"mooring-diag-v1:renewal_send",
	"mooring-diag-v1:delivery_flush",
	"mooring-diag-v1:input_flush",
	"mooring-diag-v1:batch_dispatch",
	"mooring-diag-v1:actor_receive",
	"mooring-diag-v1:owner_grant",
	"mooring-diag-v1:delivery_grant",
	"mooring-diag-v1:participant_open",
	"mooring-diag-v1:ticket",
	"mooring-diag-v1:participant_input",
	"mooring-diag-v1:commit",
	"mooring-diag-v1:unsupported_control",
	"mooring-diag-v1:delivery_owner_closed",
	"mooring-diag-v1:delivery_permission_floor",
	"mooring-diag-v1:delivery_expired",
	"mooring-diag-v1:delivery_binding",
	"mooring-diag-v1:delivery_unrelated",
	"mooring-diag-v1:actor_fatal",
}

// A bitset and position recognize allowed literals across partial writes.
// No attacker-controlled byte is copied into retained state, even temporarily.
type privateDiagnosticSink struct {
	mu         sync.Mutex
	candidates uint32
	position   int
	retained   [privateDiagnosticRetained]uint8
	count      int
	truncated  bool
	finished   bool
}

func newPrivateDiagnosticSink() *privateDiagnosticSink {
	return &privateDiagnosticSink{candidates: (1 << len(privateDiagnosticLines)) - 1}
}

func (s *privateDiagnosticSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return len(p), nil
	}
	for _, b := range p {
		if b == '\n' {
			for i, line := range privateDiagnosticLines {
				if s.candidates&(1<<i) != 0 && s.position == len(line) {
					if s.count < len(s.retained) {
						s.retained[s.count] = uint8(i + 1)
						s.count++
					} else {
						s.truncated = true
					}
					break
				}
			}
			s.candidates = (1 << len(privateDiagnosticLines)) - 1
			s.position = 0
			continue
		}
		if s.position >= privateDiagnosticLineLimit {
			s.candidates = 0
			continue // Drop until the next newline, never retain a suffix.
		}
		for i, line := range privateDiagnosticLines {
			if s.candidates&(1<<i) != 0 && (s.position >= len(line) || line[s.position] != b) {
				s.candidates &^= 1 << i
			}
		}
		s.position++
	}
	return len(p), nil
}

func (s *privateDiagnosticSink) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	s.candidates = 0
	s.position = 0 // An unterminated prefix/literal is never an event.
}

func (s *privateDiagnosticSink) snapshot() ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, s.count)
	for i := range out {
		out[i] = privateDiagnosticLines[s.retained[i]-1]
	}
	return out, s.truncated
}

// PrivateDiagnosticStages is observation ONLY in this private overlay. It never
// waits or changes cleanup. A complete snapshot is available only after the
// existing process.Done (exec.Cmd.Wait already drained the bounded stderr sink).
func (s *SessionRuntime) PrivateDiagnosticStages() ([]string, bool, bool) {
	if s == nil || s.process == nil || s.process.privateDiagnostic == nil {
		return nil, false, false
	}
	select {
	case <-s.process.Done():
		stages, truncated := s.process.privateDiagnostic.snapshot()
		return stages, truncated, true
	default:
		return nil, false, false
	}
}
