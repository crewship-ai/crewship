package mooring

import (
	"log/slog"
	"os/exec"
	"sync/atomic"
)

const runtimeStderrBytes = 8 << 10
const runtimeStderrEvents = 16

// The child never inherits backend credentials. Still treat stderr as untrusted:
// neither a document-bearing panic nor arbitrary executable output may become a
// log attribute. Only the existing Rust entrypoint/lane's closed static vocabulary
// is recognized. The fixed line buffer and lifetime input/event budgets bound work.
type runtimeStderr struct {
	logger     *slog.Logger
	runtimeRef atomic.Pointer[string]
	remaining  int
	events     int
	line       [160]byte
	length     int
	overlong   bool
	unknown    bool
}

func runtimeStderrEvidence(cmd *exec.Cmd) (func(), error) {
	cmd.Stderr = newRuntimeStderr(slog.Default())
	// exec.Cmd owns the stderr copy and drains it before its existing Wait returns.
	// No additional close, timer, runtime state or authority is introduced here.
	return func() {}, nil
}

func newRuntimeStderr(logger *slog.Logger) *runtimeStderr {
	return &runtimeStderr{logger: logger, remaining: runtimeStderrBytes}
}

func (s *runtimeStderr) Write(p []byte) (int, error) {
	n := len(p)
	if s.remaining == 0 || s.events == runtimeStderrEvents {
		return n, nil
	}
	if len(p) > s.remaining {
		p = p[:s.remaining]
	}
	s.remaining -= len(p)
	for _, b := range p {
		if s.events == runtimeStderrEvents {
			break
		}
		if b == '\n' {
			stage, kind, ok := runtimeStderrLiteral(string(s.line[:s.length]))
			if ok && !s.overlong {
				s.emit(stage, kind)
			} else if !s.unknown {
				s.unknown = true
				s.emit("unrecognized-stderr-suppressed", "none")
			}
			clear(s.line[:])
			s.length = 0
			s.overlong = false
		} else if s.length < len(s.line) && !s.overlong {
			s.line[s.length] = b
			s.length++
		} else if !s.overlong {
			clear(s.line[:])
			s.length = 0
			s.overlong = true
		}
	}
	if s.remaining == 0 && s.events < runtimeStderrEvents {
		clear(s.line[:])
		s.length = 0
		s.overlong = false
		s.emit("stderr-byte-budget-exhausted", "none")
	}
	// Diagnostics must not alter the child's original protocol/exit result.
	return n, nil
}

func (s *runtimeStderr) emit(stage, kind string) {
	s.events++
	if ref := s.runtimeRef.Load(); ref != nil {
		s.logger.Warn("Mooring Rust stderr", "runtime_ref", *ref, "stage", stage, "kind", kind)
	} else {
		s.logger.Warn("Mooring Rust stderr", "stage", stage, "kind", kind)
	}
}

func runtimeStderrLiteral(line string) (string, string, bool) {
	for _, stage := range []string{"peer-eof", "invalid-incoming-control", "already-closed", "queue-capacity"} {
		if line == "mooring session lane: "+stage+" kind=None" {
			return stage, "none", true
		}
	}
	for _, kind := range []string{
		"NotFound", "PermissionDenied", "ConnectionRefused", "ConnectionReset",
		"ConnectionAborted", "NotConnected", "AddrInUse", "AddrNotAvailable",
		"BrokenPipe", "AlreadyExists", "WouldBlock", "InvalidInput", "InvalidData",
		"TimedOut", "WriteZero", "Interrupted", "Unsupported", "UnexpectedEof",
		"OutOfMemory", "Other",
	} {
		switch {
		case line == "mooring session actor: step-error kind="+kind:
			return "actor-step-error", kind, true
		case line == "mooring session actor: owner-check-error kind="+kind:
			return "actor-owner-check-error", kind, true
		case line == "mooring session actor: maintenance-error kind="+kind:
			return "actor-maintenance-error", kind, true
		case line == "mooring runtime stopped: kind="+kind:
			return "runtime-stopped", kind, true
		case line == "mooring session lane: output-error kind="+kind:
			return "output-error", kind, true
		case line == "mooring session lane: frame-read-error kind=Some("+kind+")":
			return "frame-read-error", kind, true
		}
	}
	// Even known prefixes with unknown kind/suffix/ANSI/CR are never forwarded.
	return "", "", false
}
