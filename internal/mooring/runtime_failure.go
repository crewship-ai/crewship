package mooring

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
)

// ObserveFailure records only the first observed failure for this runtime. It
// does not close, cancel, grant rights, or change the original returned error.
// The caller supplies a closed diagnostic vocabulary, never document/error text.
func (s *SessionRuntime) ObserveFailure(stage, control string, err error) {
	if s == nil {
		return
	}
	if emit := s.claimFailure(stage, control, err); emit != nil {
		emit()
	}
}

// Claim before signalling closure; emit only after releasing runtime locks.
func (s *SessionRuntime) claimFailure(stage, control string, err error) (emit func()) {
	s.failureOnce.Do(func() {
		stage, control, category := runtimeFailureStage(stage), runtimeFailureControl(control), runtimeFailureCategory(err)
		emit = func() {
			slog.Warn("Mooring session first failure", "runtime_ref", s.runtimeRef,
				"stage", stage, "control_kind", control, "category", category)
		}
	})
	return emit
}

func runtimeFailureStage(stage string) string {
	switch stage {
	case "runtime-close", "runtime-failure", "process-exit", "rpc-read", "rpc-validate", "reply-correlation", "child-id-correlation", "control-capacity", "request-deadline", "request-write", "request-wait", "reply-write", "write-deadline", "rpc-write",
		"pump-mutation", "pump-output", "pump-management", "pump-mutation-panic", "pump-output-panic", "pump-management-panic", "pump-next-control", "pump-frame", "pump-decode", "pump-queue-capacity", "pump-callback", "pump-callback-context", "pump-response-correlation", "pump-response-marshal", "pump-reply-control", "pump-complete-control", "pump-delivery-guard", "pump-joined-callback", "pump-joined-continuation":
		return stage
	default:
		return "unclassified"
	}
}

func runtimeFailureControl(control string) string {
	switch control {
	case "session_submit_batch", "session_status_request", "owner_acquire", "owner_renew", "ticket_validate", "delivery_renew", "session_participant_output":
		return control
	default:
		return "none"
	}
}

func runtimeFailureCategory(err error) string {
	switch {
	case err == nil:
		return "unspecified"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.Is(err, ErrRPCFrame), errors.Is(err, ErrSessionRuntime):
		return "protocol"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	default:
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return "io-timeout"
		}
		return "other"
	}
}
