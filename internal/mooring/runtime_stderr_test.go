package mooring

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func stderrTestRecords(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		out = append(out, item)
	}
	return out
}

func TestRuntimeStderrStaticErrorsAndRedaction(t *testing.T) {
	var output bytes.Buffer
	sink := newRuntimeStderr(slog.New(slog.NewJSONHandler(&output, nil)))
	secret := "document-secret-must-not-enter-logs"
	for _, line := range []string{
		secret, "mooring runtime stopped: kind=" + secret,
		"mooring runtime stopped: kind=InvalidData " + secret,
		"prefix mooring runtime stopped: kind=InvalidData",
		"mooring runtime stopped: kind=InvalidData\r",
		strings.Repeat(secret, 30),
		"mooring session lane: frame-read-error kind=Some(UnexpectedEof)",
		"mooring session lane: peer-eof kind=None",
		"mooring session lane: output-error kind=BrokenPipe",
		"mooring runtime stopped: kind=InvalidData",
	} {
		raw := []byte(line + "\n")
		// Recognition must survive arbitrary stderr write boundaries.
		for _, chunk := range [][]byte{raw[:len(raw)/2], raw[len(raw)/2:]} {
			if n, err := sink.Write(chunk); n != len(chunk) || err != nil {
				t.Fatal(n, err)
			}
		}
	}
	if strings.Contains(output.String(), secret) {
		t.Fatal("untrusted stderr entered logs")
	}
	records := stderrTestRecords(t, output.String())
	stages := []string{"unrecognized-stderr-suppressed", "frame-read-error", "peer-eof", "output-error", "runtime-stopped"}
	kinds := []string{"none", "UnexpectedEof", "none", "BrokenPipe", "InvalidData"}
	if len(records) != len(stages) {
		t.Fatalf("unexpected records: %s", output.String())
	}
	for i, r := range records {
		if r["stage"] != stages[i] || r["kind"] != kinds[i] {
			t.Fatalf("unexpected static record: %v", r)
		}
	}
}

func TestRuntimeStderrLifetimeBudgetsAndPartialLines(t *testing.T) {
	var output bytes.Buffer
	sink := newRuntimeStderr(slog.New(slog.NewJSONHandler(&output, nil)))
	p := []byte(strings.Repeat("mooring runtime stopped: kind=InvalidData\n", 1000))
	for range 3 {
		if n, err := sink.Write(p); n != len(p) || err != nil {
			t.Fatal(n, err)
		}
	}
	if got := len(stderrTestRecords(t, output.String())); got != runtimeStderrEvents {
		t.Fatalf("event budget: %d", got)
	}
	output.Reset()
	sink = newRuntimeStderr(slog.New(slog.NewJSONHandler(&output, nil)))
	p = bytes.Repeat([]byte{'x'}, runtimeStderrBytes*2)
	if n, err := sink.Write(p); n != len(p) || err != nil {
		t.Fatal(n, err)
	}
	_, _ = sink.Write([]byte("\nmooring runtime stopped: kind=InvalidData\n"))
	records := stderrTestRecords(t, output.String())
	if sink.remaining != 0 || len(records) != 1 || records[0]["stage"] != "stderr-byte-budget-exhausted" {
		t.Fatal("byte budget failed", records)
	}
	output.Reset()
	sink = newRuntimeStderr(slog.New(slog.NewJSONHandler(&output, nil)))
	_, _ = sink.Write([]byte("mooring runtime stopped: kind=InvalidData"))
	if output.Len() != 0 {
		t.Fatal("unterminated stderr was logged")
	}
}

func TestRuntimeStderrActorErrorsAreRuntimeCorrelatedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	sink := newRuntimeStderr(slog.New(slog.NewJSONHandler(&output, nil)))
	ref := "host-generated-runtime-ref"
	sink.runtimeRef.Store(&ref)
	_, _ = sink.Write([]byte("mooring session actor: step-error kind=InvalidData\nmooring session actor: owner-check-error kind=TimedOut\nmooring session actor: maintenance-error kind=BrokenPipe\nmooring session actor: step-error kind=InvalidData private-checkpoint\n"))
	rows := stderrTestRecords(t, output.String())
	if len(rows) != 4 || rows[0]["stage"] != "actor-step-error" || rows[1]["stage"] != "actor-owner-check-error" || rows[2]["stage"] != "actor-maintenance-error" || rows[3]["stage"] != "unrecognized-stderr-suppressed" || strings.Contains(output.String(), "private-checkpoint") {
		t.Fatal(output.String())
	}
	for _, row := range rows {
		if row["runtime_ref"] != ref {
			t.Fatal(row)
		}
	}
}
