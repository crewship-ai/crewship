//go:build !clionly

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestEmitDoctorJSON_Shape pins the documented JSON contract: top-
// level object with checks[], failed, warned, version, os, arch.
// Mostly drives the encoder with hand-built results so the test
// doesn't have to stand up the full check battery (which touches
// Docker, the data dir, the network, and the DB).
func TestEmitDoctorJSON_Shape(t *testing.T) {
	t.Parallel()
	results := []checkResult{
		{name: "container runtime", status: "PASS", detail: "docker 24.0 (/var/run/docker.sock)"},
		{name: "data directory", status: "PASS", detail: "/home/op/.crewship"},
		{name: "db migration version", status: "WARN", detail: "98 (latest is 99)", hint: "run 'crewship migrate'"},
		{name: "server reachable", status: "FAIL", detail: "connection refused on http://localhost:8080", hint: "is 'crewship start' running?"},
		{name: "telemetry status", status: "INFO", detail: "OTLP disabled (CREWSHIP_TELEMETRY_OTLP_ENDPOINT unset)"},
	}
	buf := &bytes.Buffer{}
	emitDoctorJSON(buf, results, 1, 1)

	var got doctorJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, buf.String())
	}

	if got.Failed != 1 {
		t.Errorf("failed = %d, want 1", got.Failed)
	}
	if got.Warned != 1 {
		t.Errorf("warned = %d, want 1", got.Warned)
	}
	if len(got.Checks) != 5 {
		t.Fatalf("checks len = %d, want 5", len(got.Checks))
	}
	if got.Checks[3].Status != "FAIL" {
		t.Errorf("checks[3].status = %q, want FAIL", got.Checks[3].Status)
	}
	if got.Checks[3].Hint != "is 'crewship start' running?" {
		t.Errorf("FAIL check missing hint round-trip: %q", got.Checks[3].Hint)
	}
	// os/arch are runtime-derived; just assert they're populated.
	if got.OS == "" {
		t.Error("os field empty")
	}
	if got.Arch == "" {
		t.Error("arch field empty")
	}
}

// TestEmitDoctorJSON_OmitsEmptyHint pins the omitempty contract on
// hint. A check with no hint MUST NOT emit `"hint": ""` so a strict-
// schema consumer can branch on presence rather than emptiness.
func TestEmitDoctorJSON_OmitsEmptyHint(t *testing.T) {
	t.Parallel()
	results := []checkResult{
		{name: "x", status: "PASS", detail: "ok"}, // no hint
	}
	buf := &bytes.Buffer{}
	emitDoctorJSON(buf, results, 0, 0)
	if bytes.Contains(buf.Bytes(), []byte(`"hint"`)) {
		t.Errorf("emitted hint key when value is empty: %s", buf.String())
	}
}

// TestEmitDoctorJSON_EmptyResults still produces a valid object with
// checks: [] (NOT null). CI consumers that iterate the array would
// trip on null but handle empty arrays correctly.
func TestEmitDoctorJSON_EmptyResults(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	emitDoctorJSON(buf, nil, 0, 0)

	// Don't decode through doctorJSON — that allows null to satisfy
	// the slice. Instead read the raw JSON and check the key.
	if !bytes.Contains(buf.Bytes(), []byte(`"checks": []`)) {
		t.Errorf("expected empty checks array, got %s", buf.String())
	}
}

// TestDoctorCmd_JSONFlag guards the flag wiring on the cobra command.
func TestDoctorCmd_JSONFlag(t *testing.T) {
	t.Parallel()
	f := doctorCmd.Flags().Lookup("json")
	if f == nil {
		t.Fatal("crewship doctor missing --json flag")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--json type = %s, want bool", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--json default = %s, want false (human is default)", f.DefValue)
	}
}

// TestDoctorCmd_LongDocumentsJSON pins the Long string to mention
// the JSON output shape so a refactor that hides the contract from
// users surfaces in tests, not in support tickets.
func TestDoctorCmd_LongDocumentsJSON(t *testing.T) {
	t.Parallel()
	long := doctorCmd.Long
	for _, mustHave := range []string{
		"--json",
		"\"checks\":",
		"\"failed\":",
		"\"warned\":",
		"PASS / WARN / FAIL / INFO",
	} {
		if !strings.Contains(long, mustHave) {
			t.Errorf("doctor Long missing %q — contract drift", mustHave)
		}
	}
}
