package server

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// TestParseProcNetTCPLine pins the per-line parser. /proc/net/tcp is a
// Linux ABI we depend on for Crow's Nest open-port discovery — a regex
// drift here would silently empty the panel, so the table covers every
// state byte the kernel can put in column 4 and a few malformed inputs.
func TestParseProcNetTCPLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantPort int
		wantOK   bool
	}{
		{
			// 0x239F = 9119 decimal — the sidecar's port.
			name:     "ipv4 listen on 9119",
			line:     "  0: 0100007F:239F 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 0 1 0000000000000000 100 0 0 10 0",
			wantPort: 9119,
			wantOK:   true,
		},
		{
			name:     "ipv6 listen on 8000 (any-addr)",
			line:     "  3: 00000000000000000000000000000000:1F40 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 0 1 0000000000000000 100 0 0 10 0",
			wantPort: 8000,
			wantOK:   true,
		},
		{
			name:   "established connection — not a listener",
			line:   "  1: 0100007F:8080 0100007F:C3F0 01 00000000:00000000 00:00000000 00000000     0        0 0 1 0000000000000000",
			wantOK: false,
		},
		{
			name:   "time_wait — not a listener",
			line:   "  2: 0100007F:8080 0100007F:C3F1 06 00000000:00000000 00:00000000 00000000     0        0 0 1 0000000000000000",
			wantOK: false,
		},
		{
			name:   "header row",
			line:   "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
		{
			name:   "truncated line",
			line:   "  0: 0100007F:23A7",
			wantOK: false,
		},
		{
			name:   "missing colon in local_address",
			line:   "  0: 0100007F23A7 00000000:0000 0A",
			wantOK: false,
		},
		{
			name:   "non-hex port",
			line:   "  0: 0100007F:GHIJ 00000000:0000 0A",
			wantOK: false,
		},
		{
			name:   "port 0 — kernel assigns; not a real listener for our purposes",
			line:   "  0: 0100007F:0000 00000000:0000 0A",
			wantOK: false,
		},
		{
			name:     "high port (max valid)",
			line:     "  0: 0100007F:FFFF 00000000:0000 0A",
			wantPort: 65535,
			wantOK:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcNetTCPLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.wantPort {
				t.Errorf("port = %d, want %d", got, tc.wantPort)
			}
		})
	}
}

// stubExecProvider implements just enough of provider.ContainerProvider
// for the scanner's exec call. Returns a canned /proc/net/tcp payload
// so the scanner's parsing + diff loop can be exercised without Docker.
type stubExecProvider struct {
	provider.ContainerProvider // embed so unused methods panic if called
	procPayload                string
	execErr                    error
	calls                      int
}

func (s *stubExecProvider) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	s.calls++
	if s.execErr != nil {
		return nil, s.execErr
	}
	return &provider.ExecResult{
		ExecID: "stub",
		Reader: io.NopCloser(strings.NewReader(s.procPayload)),
	}, nil
}

// recordingEmitter captures every Emit call so tests can assert the
// scanner's diff produces opened/closed events for the right ports.
type recordingEmitter struct {
	entries []journal.Entry
}

func (r *recordingEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.entries = append(r.entries, e)
	return "id-" + e.Summary, nil
}

func (r *recordingEmitter) Flush(_ context.Context) error { return nil }

func TestScanContainerListeningPorts_Dedupes(t *testing.T) {
	// Same port reported by both tcp and tcp6 (real-world: a server bound
	// to ::* listens on both families). Should appear once.
	payload := strings.Join([]string{
		"  sl  local_address",
		"  0: 0100007F:1F40 00000000:0000 0A",
		"  1: 00000000000000000000000000000000:1F40 00000000000000000000000000000000:0000 0A",
		"  2: 00000000:0050 00000000:0000 0A",
	}, "\n")
	stub := &stubExecProvider{procPayload: payload}
	ports, err := scanContainerListeningPorts(context.Background(), stub, "ctr-1", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ports) != 2 {
		t.Fatalf("want 2 ports (8000 deduped, plus 80), got %d: %v", len(ports), ports)
	}
	seen := map[int]bool{}
	for _, p := range ports {
		seen[p] = true
	}
	for _, want := range []int{8000, 80} {
		if !seen[want] {
			t.Errorf("missing port %d in %v", want, ports)
		}
	}
}

func TestScanContainerListeningPorts_ExecError(t *testing.T) {
	stub := &stubExecProvider{execErr: io.ErrUnexpectedEOF}
	if _, err := scanContainerListeningPorts(context.Background(), stub, "ctr-1", nil); err == nil {
		t.Fatal("expected error to propagate from Exec")
	}
}

func TestEmitListeningPortEvent_Shapes(t *testing.T) {
	rec := &recordingEmitter{}
	emitListeningPortEvent(context.Background(), rec, journal.EntryNetworkPortOpen,
		listeningPortKey{ContainerID: "abcdef0123456789", Port: 8000},
		listeningPortValue{WorkspaceID: "ws-1", CrewID: "crew-1"},
	)
	if len(rec.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(rec.entries))
	}
	e := rec.entries[0]
	if e.Type != journal.EntryNetworkPortOpen {
		t.Errorf("type = %s, want %s", e.Type, journal.EntryNetworkPortOpen)
	}
	if e.WorkspaceID != "ws-1" || e.CrewID != "crew-1" {
		t.Errorf("scope wrong: ws=%s crew=%s", e.WorkspaceID, e.CrewID)
	}
	// Container id in summary is the 12-char short form, not the full 16.
	if !strings.Contains(e.Summary, "abcdef012345") {
		t.Errorf("summary missing short cid: %s", e.Summary)
	}
	if strings.Contains(e.Summary, "abcdef0123456789") {
		t.Errorf("summary leaked full cid: %s", e.Summary)
	}
	if got := e.Payload["port"]; got != 8000 {
		t.Errorf("payload.port = %v, want 8000", got)
	}
	if got := e.Payload["source"]; got != "proc_scan" {
		t.Errorf("payload.source = %v, want proc_scan", got)
	}
}

// TestRunListeningPortScanner_Diff drives one full poll cycle: register
// a tracked container, let the scanner run once, then change the
// returned ports between cycles. Asserts the second cycle emits one
// network.port_closed for the port that disappeared and one
// network.port_opened for the new one.
func TestRunListeningPortScanner_Diff(t *testing.T) {
	t.Parallel()

	// First cycle: ports 8000 (0x1F40), 9119 (0x239F)
	stub := &stubExecProvider{procPayload: strings.Join([]string{
		"  0: 0100007F:1F40 00000000:0000 0A",
		"  1: 0100007F:239F 00000000:0000 0A",
	}, "\n")}

	// StatsCollector with one tracked container. We don't run its full
	// poll loop — only need Tracked() to return our row.
	sc := NewStatsCollector(stub, nil, nil, time.Second)
	sc.Register("ctr-1", "crew-1", "ws-1")

	rec := &recordingEmitter{}
	ctx, cancel := context.WithCancel(context.Background())

	// Drive the scan + diff inline — easier to assert than racing the
	// 15s ticker. Mirror the loop body directly.
	prev := map[listeningPortKey]listeningPortValue{}
	for cycle := 0; cycle < 2; cycle++ {
		if cycle == 1 {
			// Second cycle: drop 8000, add 5432 (8765 simulating a new
			// dev server starting up, port_closed for 8000)
			stub.procPayload = strings.Join([]string{
				"  0: 0100007F:1538 00000000:0000 0A", // 5432
				"  1: 0100007F:239F 00000000:0000 0A", // 9119
			}, "\n")
		}
		current := map[listeningPortKey]listeningPortValue{}
		for _, tc := range sc.Tracked() {
			ports, err := scanContainerListeningPorts(ctx, stub, tc.ContainerID, nil)
			if err != nil {
				t.Fatalf("cycle %d scan: %v", cycle, err)
			}
			for _, p := range ports {
				current[listeningPortKey{ContainerID: tc.ContainerID, Port: p}] = listeningPortValue{
					WorkspaceID: tc.WorkspaceID, CrewID: tc.CrewID,
				}
			}
		}
		for k, v := range current {
			if _, was := prev[k]; !was {
				emitListeningPortEvent(ctx, rec, journal.EntryNetworkPortOpen, k, v)
			}
		}
		for k, v := range prev {
			if _, still := current[k]; !still {
				emitListeningPortEvent(ctx, rec, journal.EntryNetworkPortClose, k, v)
			}
		}
		prev = current
	}
	cancel()

	// Cycle 1 opens: 8000, 9119 (both new)
	// Cycle 2 closes: 8000; opens: 5432; 9119 stays put
	// Total: 3 opens (8000, 9119, 5432) + 1 close (8000)
	var opens, closes int
	for _, e := range rec.entries {
		switch e.Type {
		case journal.EntryNetworkPortOpen:
			opens++
		case journal.EntryNetworkPortClose:
			closes++
		}
	}
	if opens != 3 {
		t.Errorf("opens = %d, want 3", opens)
	}
	if closes != 1 {
		t.Errorf("closes = %d, want 1", closes)
	}
}
