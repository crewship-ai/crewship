package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestCollaborationSocketTerminationDiagnostics(t *testing.T) {
	const secret = "peer-token-and-payload-must-never-appear"
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"read timeout", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, "read_timeout"},
		{"wrapped timeout", fmt.Errorf("%s: %w", secret, os.ErrDeadlineExceeded), "read_timeout"},
		{"peer close", &websocket.CloseError{Code: websocket.CloseNormalClosure, Text: secret}, "peer_closed"},
		{"wrapped peer close", fmt.Errorf("%s: %w", secret, &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: secret}), "peer_closed"},
		{"transport error", errors.New(secret), "transport_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := collaborationSocketCloseReason(tc.err)
			if reason != tc.want {
				t.Fatalf("reason=%q want%q", reason, tc.want)
			}
			var output bytes.Buffer
			collaborationLogSocketTermination(slog.New(slog.NewJSONHandler(&output, nil)), "host-generated-ref", "active", reason)
			if strings.Contains(output.String(), secret) {
				t.Fatal("error text leaked")
			}
			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["connection_ref"] != "host-generated-ref" || entry["phase"] != "active" || entry["closed_reason"] != tc.want || entry["time"] == "" {
				t.Fatalf("missing correlation fields: %v", entry)
			}
			if len(entry) != 6 {
				t.Fatalf("unexpected log fields: %v", entry)
			}
		})
	}
	t.Run("fixed vocabulary", func(t *testing.T) {
		for _, reason := range []string{"read_timeout", "peer_closed", "session_closed", "transport_error", "ping_write_failed", secret} {
			var output bytes.Buffer
			collaborationLogSocketTermination(slog.New(slog.NewJSONHandler(&output, nil)), "host-generated-ref", "join", reason)
			if strings.Contains(output.String(), secret) {
				t.Fatal("untrusted reason leaked")
			}
			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			want := reason
			if reason == secret {
				want = "transport_error"
			}
			if entry["closed_reason"] != want || entry["phase"] != "join" {
				t.Fatal(entry)
			}
		}
		collaborationLogSocketTermination(nil, "host-generated-ref", "join", "transport_error")
	})
}
