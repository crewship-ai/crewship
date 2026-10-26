package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/crewship-ai/crewship/internal/mooring"
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

func TestCollaborationIngressDiagnosticsRedactionAndCategories(t *testing.T) {
	const secret = "peer-raw-payload-ticket-secret"
	deadline, cancel := context.WithCancelCause(t.Context())
	cancel(context.DeadlineExceeded) // canceled context alone must not invent a timeout
	if got := collaborationIngressCategory(context.Canceled, deadline, nil); got != "canceled" {
		t.Fatal(got)
	}
	expired, stop := context.WithTimeout(t.Context(), 0)
	defer stop()
	cases := []struct {
		name, stage, capacity, want string
		err                         error
		ctx                         context.Context
	}{
		{"nested bytes", "capture_public_capacity", "capacity_bytes", "capacity_bytes", fmt.Errorf("%s: %w", secret, mooring.ErrBatchCapacity), t.Context()},
		{"records", "reserve_capacity", "capacity_records", "capacity_records", mooring.ErrBatchCapacity, t.Context()},
		{"deadline cancellation bridge", "capture_seal", "", "deadline", context.Canceled, expired},
		{"send deadline", "send_original_runtime", "", "deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), t.Context()},
		{"capacity precedes elapsed context", "reserve_capacity", "capacity_bytes", "capacity_bytes", mooring.ErrBatchCapacity, expired},
		{"opaque rejection", "active_current_gate", "", "rejected", errors.New(secret), t.Context()},
		{"rejection stays rejection after deadline", "capture_seal", "", "rejected", errors.New(secret), expired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &collaborationIngressDiagnostic{stage: tc.stage, capacity: tc.capacity}
			category := collaborationIngressCategory(tc.err, tc.ctx, d)
			if category != tc.want {
				t.Fatalf("category %s", category)
			}
			var output bytes.Buffer
			collaborationLogIngressFailure(slog.New(slog.NewJSONHandler(&output, nil)), "connection-ref", "admission-ref", "runtime-ref", d.stage, category)
			if strings.Contains(output.String(), secret) {
				t.Fatal("raw content leaked")
			}
			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["stage"] != tc.stage || entry["category"] != tc.want || entry["connection_ref"] != "connection-ref" || entry["admission_ref"] != "admission-ref" || entry["runtime_ref"] != "runtime-ref" || len(entry) != 8 {
				t.Fatal(entry)
			}
		})
	}
	var output bytes.Buffer
	collaborationLogIngressFailure(slog.New(slog.NewJSONHandler(&output, nil)), "connection-ref", "admission-ref", "runtime-ref", secret, secret)
	if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), `"stage":"unclassified"`) {
		t.Fatal("unbounded vocabulary")
	}
	var d *collaborationIngressDiagnostic
	d.at(secret)
	d.capacityAt(secret, secret)
	collaborationLogIngressFailure(nil, "", "", "", secret, secret)
	var c *collaborationHTTPConnection
	c.logIngressFailure(secret, secret)
}

func TestCollaborationIngressReserveCapacityObservation(t *testing.T) {
	for _, mode := range []string{"bytes", "records", "both"} {
		t.Run(mode, func(t *testing.T) {
			l, c := originalSlotDataFixture()
			if err := l.observe(c, collaborationObservedCut{"0", "sha256:" + strings.Repeat("0", 64), ""}); err != nil {
				t.Fatal(err)
			}
			if mode == "bytes" || mode == "both" {
				l.bytes = collaborationOriginalByteLimit
			}
			if mode == "records" || mode == "both" {
				for i := 0; i < collaborationOriginalRecordLimit; i++ {
					l.changes[fmt.Sprint(i)] = &collaborationLiveOriginal{}
				}
			}
			beforeBytes, beforeRecords := l.bytes, len(l.changes)
			d := &collaborationIngressDiagnostic{}
			_, _, err := l.reserveObserved(c, []mooring.BatchChange{originalTestChange(t, c, "overflow", "0", "", "AQ==")}, d)
			want := "capacity_" + mode
			if mode == "both" {
				want = "capacity_records"
			}
			if !errors.Is(err, mooring.ErrBatchCapacity) || d.stage != "reserve_capacity" || collaborationIngressCategory(err, t.Context(), d) != want {
				t.Fatalf("wrong diagnostic: %s/%s", d.stage, d.capacity)
			}
			if l.bytes != beforeBytes || len(l.changes) != beforeRecords {
				t.Fatal("diagnostic changed reservation on failure")
			}
		})
	}
}
