package mooring

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestSessionRuntimeFailureObservationIsOnceBoundedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	s := &SessionRuntime{runtimeRef: "host-generated-ref"}
	secret := "private-document-error-ticket"
	s.ObserveFailure("pump-callback", "session_submit_batch", errors.New(secret))
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() { s.ObserveFailure(secret, secret, context.DeadlineExceeded) })
	}
	group.Wait()
	if s.closed || s.err != nil {
		t.Fatal("observation changed transport lifecycle")
	}
	rows := stderrTestRecords(t, output.String())
	if len(rows) != 1 || rows[0]["runtime_ref"] != s.runtimeRef || rows[0]["stage"] != "pump-callback" || rows[0]["control_kind"] != "session_submit_batch" || rows[0]["category"] != "other" || strings.Contains(output.String(), secret) {
		t.Fatalf("unbounded or unqualified event: %s", output.String())
	}
}

func TestSessionRuntimeFailureClaimPrecedesWakeAndEmitsOutsideLock(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	s := &SessionRuntime{runtimeRef: "host-generated-ref"}
	s.mu.Lock()
	emit := s.claimFailure("rpc-read", "", io.EOF)
	if output.Len() != 0 {
		t.Fatal("diagnostic performed I/O under runtime lock")
	}
	s.mu.Unlock()
	s.ObserveFailure("runtime-close", "", ErrSessionRuntime)
	emit()
	rows := stderrTestRecords(t, output.String())
	if len(rows) != 1 || rows[0]["stage"] != "rpc-read" || rows[0]["category"] != "eof" {
		t.Fatal(rows)
	}
}

func TestSessionRuntimeFailureVocabulary(t *testing.T) {
	var absent *SessionRuntime
	absent.ObserveFailure("pump-response-correlation", "ticket_validate", context.Canceled)
	if runtimeFailureStage("private frame") != "unclassified" || runtimeFailureControl("private frame") != "none" {
		t.Fatal("untrusted vocabulary escaped")
	}
	for _, item := range []struct {
		err      error
		category string
	}{{nil, "unspecified"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline"}, {io.ErrUnexpectedEOF, "eof"}, {ErrSessionRuntime, "protocol"}, {ErrRPCFrame, "protocol"}, {errors.New("secret"), "other"}} {
		if got := runtimeFailureCategory(item.err); got != item.category {
			t.Fatal(got, item.category)
		}
	}
}
