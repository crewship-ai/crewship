package apple

import (
	"bytes"
	"io"
	"sync"
)

// maxExecSpoolBytes caps how much unread output one exec may hold.
//
// The cap only bites when nobody is reading: a caller that drains keeps the
// buffer near empty, because Read consumes it. It exists so an exec whose
// caller took a 64 KiB prefix and walked away cannot grow without bound —
// which is the memory ceiling the old synchronous pipe got by blocking the
// process instead, at the cost of the process never finishing.
const maxExecSpoolBytes = 8 << 20 // 8 MiB

// execSpool is the combined stdout/stderr sink of a running exec, and the
// io.ReadCloser handed to the caller.
//
// It exists because the exit code must be a fact about the process, not about
// the caller's reading habits. Exec used to give the caller the read half of a
// synchronous io.Pipe that the process wrote into, so cmd.Wait() could not
// return until the caller drained to EOF: a caller that stopped short left the
// exec pinned at running=true/exitCode=0 forever, and a caller that closed the
// reader early made Wait() fail with io.ErrClosedPipe — not an *exec.ExitError,
// so the real status was replaced by -1.
//
// Writes therefore never block and never fail: the process always runs to
// completion and always yields a true exit status. Reads still stream — a
// caller that drains gets every byte, blocking until output arrives or the
// process ends.
type execSpool struct {
	mu      sync.Mutex
	notify  sync.Cond
	buf     bytes.Buffer
	eof     bool  // the process closed its output
	closed  bool  // the caller closed the reader
	dropped int64 // bytes discarded past the cap or after Close
}

func newExecSpool() *execSpool {
	s := &execSpool{}
	s.notify.L = &s.mu
	return s
}

// Write accepts the process's output. It reports a full write even when it
// discards the bytes, because failing a write here would surface as an I/O
// error on cmd.Wait() and destroy the exit status we exist to preserve.
func (s *execSpool) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.buf.Len() >= maxExecSpoolBytes {
		s.dropped += int64(len(p))
		return len(p), nil
	}
	s.buf.Write(p)
	s.notify.Broadcast()
	return len(p), nil
}

// closeWrite marks the end of the process's output, waking any blocked reader
// so it observes EOF once the buffer is drained.
func (s *execSpool) closeWrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eof = true
	s.notify.Broadcast()
}

func (s *execSpool) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for s.buf.Len() == 0 && !s.eof && !s.closed {
		s.notify.Wait()
	}
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if s.buf.Len() == 0 {
		return 0, io.EOF
	}
	return s.buf.Read(p)
}

// Close releases whatever the caller did not read. The process is unaffected:
// it keeps running and its exit status stays recoverable through ExecInspect.
func (s *execSpool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.dropped += int64(s.buf.Len())
	s.buf.Reset()
	s.notify.Broadcast()
	return nil
}

// droppedBytes reports output that never reached the caller, so the provider
// can say so rather than silently shortening a stream.
func (s *execSpool) droppedBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}
