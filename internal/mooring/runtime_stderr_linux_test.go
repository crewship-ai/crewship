//go:build linux

package mooring

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type runtimeStderrCapture struct {
	sync.Mutex
	data  bytes.Buffer
	ready chan struct{}
}

func (s *runtimeStderrCapture) Write(p []byte) (int, error) {
	s.Lock()
	defer s.Unlock()
	n, err := s.data.Write(p)
	select {
	case s.ready <- struct{}{}:
	default:
	}
	return n, err
}
func (s *runtimeStderrCapture) text() string { s.Lock(); defer s.Unlock(); return s.data.String() }

func TestRuntimeProductionStderrStartupFailureAndClose(t *testing.T) {
	// Explicitly select production logging even in an integration-tagged build.
	t.Setenv("MOORING_SESSION_OUTPUT_DIR", "")
	old := slog.Default()
	defer slog.SetDefault(old)
	capture := &runtimeStderrCapture{ready: make(chan struct{}, 4)}
	slog.SetDefault(slog.New(slog.NewJSONHandler(capture, nil)))
	if r, err := helperRuntime(t, t.Context(), "stderr-before-ready"); err == nil || r != nil {
		t.Fatal("child startup failure changed", err)
	}
	if raw := capture.text(); !strings.Contains(raw, "runtime-stopped") || strings.Contains(raw, "private-document") {
		t.Fatal("startup stderr missing or leaked", raw)
	}
	for {
		select {
		case <-capture.ready:
			continue
		default:
			goto drained
		}
	}
drained:
	r, err := helperRuntime(t, t.Context(), "stderr-live")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-capture.ready:
	case <-time.After(3 * time.Second):
		_ = r.Close()
		t.Fatal("live child stderr not logged")
	}
	closed := make(chan struct{})
	go func() { _ = r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stderr drain changed runtime Close lifetime")
	}
	if raw := capture.text(); !strings.Contains(raw, "frame-read-error") || strings.Contains(raw, "private-document") {
		t.Fatal("live stderr missing or leaked", raw)
	}
}
