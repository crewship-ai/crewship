package api

import (
	"errors"
	"log/slog"
	"net"

	"github.com/gorilla/websocket"
)

// Error text may contain peer-controlled close reasons or network details.
// Only the fixed classification below is eligible for production logs.
func collaborationSocketCloseReason(err error) string {
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "read_timeout"
	}
	var closed *websocket.CloseError
	if errors.As(err, &closed) {
		return "peer_closed"
	}
	return "transport_error"
}

func collaborationLogSocketTermination(logger *slog.Logger, ref, phase, reason string) {
	if logger == nil {
		return
	}
	// Bound diagnostics to their fixed vocabulary even if a future caller passes
	// an untrusted error string by mistake. Connection refs are host-generated.
	if phase != "join" && phase != "active" {
		phase = "join"
	}
	switch reason {
	case "read_timeout", "peer_closed", "session_closed", "transport_error", "ping_write_failed":
	default:
		reason = "transport_error"
	}
	logger.Info("mooring websocket terminated", "connection_ref", ref, "phase", phase, "closed_reason", reason)
}
