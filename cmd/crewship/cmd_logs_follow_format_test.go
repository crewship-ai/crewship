package main

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// `crewship logs <agent> --follow -f json` must not emit a JSON array.
//
// A follow is an unbounded stream, so the array's closing bracket would be
// written when the follow ends — which is never. Emitting one anyway produces
// a document that is an array followed by loose objects: unparseable by `jq`,
// and unparseable by anything else, from the first byte of the second shape
// onwards.
//
// This is the same defect class the rest of this change removes (a command
// advertising `-f json` and then emitting something that is not JSON), so it
// gets its own guard rather than riding on the generic format contract — the
// generic sweep cannot drive `--follow`, because a follow does not terminate.
//
// The backlog is therefore written in the requested format's streaming shape —
// one JSON object per line for json/ndjson, one YAML document per row for yaml
// — matching the live tail, so both halves of the stream agree and a consumer
// reads the whole thing with one parser.
//
// The yaml case is not decoration. Both halves originally routed every machine
// format through the JSON row writer, so `-f yaml` silently produced JSON:
// asked for one format, got another, which is the exact defect this sweep
// exists to remove.
func TestLogsFollow_MachineFormatIsNDJSONNotArray(t *testing.T) {
	backlog := []map[string]string{
		{"ts": "2026-06-10T10:00:00Z", "level": "info", "agent": "viktor", "event": "output", "content": "first"},
		{"ts": "2026-06-10T10:00:01Z", "level": "info", "agent": "viktor", "event": "output", "content": "second"},
	}

	for _, format := range []string{"json", "ndjson", "yaml"} {
		t.Run(format, func(t *testing.T) {
			s := covStubCli9(t)
			s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))
			s.OnGet("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", clitest.JSONResponse(200, backlog))

			covSetFlagCli9(t, logsCmd, "lines", "25")
			covSetFlagCli9(t, logsCmd, "follow", "true")

			orig := flagFormat
			flagFormat = format
			t.Cleanup(func() { flagFormat = orig })

			// The stub does not speak websocket, so the follow itself fails.
			// That is fine and deliberate: the backlog is written before the
			// upgrade is attempted, so stdout still carries what we assert on.
			out := covCaptureStdoutCli9(t, func() {
				_ = logsCmd.RunE(logsCmd, []string{"viktor"})
			})

			trimmed := strings.TrimSpace(out)
			if trimmed == "" {
				t.Fatalf("--follow -f %s wrote no backlog at all", format)
			}
			if strings.HasPrefix(trimmed, "[") {
				t.Errorf("--follow -f %s opened a JSON array whose bracket can never close:\n%s", format, out)
			}

			// Each format has its own streaming shape and they are not
			// interchangeable — routing yaml through the JSON row writer is
			// how `-f yaml` came to silently emit JSON.
			if format == "yaml" {
				docs := strings.Split(trimmed, "---")
				var got []map[string]interface{}
				for _, d := range docs {
					if strings.TrimSpace(d) == "" {
						continue
					}
					var row map[string]interface{}
					if err := yaml.Unmarshal([]byte(d), &row); err != nil {
						t.Errorf("--follow -f yaml document is not YAML: %v\n%s", err, d)
						continue
					}
					if strings.Contains(d, `"content"`) {
						t.Errorf("--follow -f yaml emitted JSON, not YAML:\n%s", d)
					}
					got = append(got, row)
				}
				if len(got) != len(backlog) {
					t.Fatalf("--follow -f yaml wrote %d documents, want %d:\n%s", len(got), len(backlog), out)
				}
				for i, row := range got {
					if row["content"] != backlog[i]["content"] {
						t.Errorf("--follow -f yaml doc %d content = %v, want %q", i, row["content"], backlog[i]["content"])
					}
				}
				return
			}

			// Every non-empty line must stand alone as one object.
			lines := strings.Split(trimmed, "\n")
			if len(lines) != len(backlog) {
				t.Fatalf("--follow -f %s wrote %d lines, want %d (one per backlog entry):\n%s",
					format, len(lines), len(backlog), out)
			}
			for i, l := range lines {
				var row map[string]interface{}
				if err := json.Unmarshal([]byte(l), &row); err != nil {
					t.Errorf("--follow -f %s line %d is not a standalone JSON object: %v\n%s", format, i, err, l)
					continue
				}
				if row["content"] != backlog[i]["content"] {
					t.Errorf("--follow -f %s line %d content = %v, want %q", format, i, row["content"], backlog[i]["content"])
				}
			}
		})
	}
}

// The control: WITHOUT --follow the result is a finite document, so the array
// is correct and must not regress into NDJSON.
func TestLogs_NoFollow_MachineFormatStaysAnArray(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, covLogsAgents()))
	s.OnGet("/api/v1/agents/cagentaaaaaaaaaaaaaaaa/logs", clitest.JSONResponse(200, []map[string]string{
		{"ts": "2026-06-10T10:00:00Z", "level": "info", "agent": "viktor", "event": "output", "content": "only"},
	}))

	covSetFlagCli9(t, logsCmd, "lines", "25")
	covSetFlagCli9(t, logsCmd, "follow", "false")

	orig := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = orig })

	out := covCaptureStdoutCli9(t, func() {
		if err := logsCmd.RunE(logsCmd, []string{"viktor"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("logs -f json (no --follow) is not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["content"] != "only" {
		t.Errorf("logs -f json returned %v, want one row with content %q", rows, "only")
	}
}
