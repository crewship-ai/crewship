//go:build integration && linux

package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// Docker Exec combines stdout and stderr; the CLI deliberately writes its
// decision on stdout and its nonzero-exit envelope on stderr in JSON mode.
func decodeNativeKeeperCrewReply(raw []byte, code int, reply *nativeCrewKeeperReply) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decisions, diagnostics := 0, 0
	for {
		var object map[string]json.RawMessage
		if err := decoder.Decode(&object); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("malformed combined CLI stream: %w", err)
		}
		if decision, ok := object["decision"]; ok {
			if _, ambiguous := object["error"]; ambiguous {
				return fmt.Errorf("ambiguous decision and error document")
			}
			decisions++
			if decisions != 1 {
				return fmt.Errorf("duplicate Keeper decision document")
			}
			encoded, err := json.Marshal(object)
			if err != nil || json.Unmarshal(encoded, reply) != nil || reply.RequestID == "" {
				return fmt.Errorf("invalid Keeper decision identity")
			}
			var name string
			if json.Unmarshal(decision, &name) != nil || (name != "ALLOW" && name != "DENY" && name != "ESCALATE" && name != "PENDING") {
				return fmt.Errorf("unrecognized Keeper decision")
			}
			continue
		}
		diagnostic, ok := object["error"]
		if !ok || len(object) != 1 {
			return fmt.Errorf("unexpected CLI document")
		}
		var body struct {
			Message  string `json:"message"`
			ExitCode int    `json:"exit_code"`
		}
		if json.Unmarshal(diagnostic, &body) != nil || body.Message == "" || body.ExitCode != code || code == 0 {
			return fmt.Errorf("CLI diagnostic does not match observed exit")
		}
		diagnostics++
		if diagnostics != 1 {
			return fmt.Errorf("duplicate CLI error document")
		}
	}
	if decisions != 1 || (code == 0 && diagnostics != 0) || (code != 0 && diagnostics != 1) {
		return fmt.Errorf("missing or contradictory CLI execution evidence")
	}
	if reply.Decision == "ALLOW" && code != 0 && (reply.ExitCode == nil || *reply.ExitCode != code) {
		return fmt.Errorf("Keeper tool exit does not match CLI process exit")
	}
	return nil
}

func TestNativeKeeperCrewReplyMergedStreams(t *testing.T) {
	stdout := `{"request_id":"fixture-request","decision":"ALLOW","exit_code":1}`
	stderr := `{"error":{"message":"Keeper command exited with code 1","exit_code":1}}`
	for _, raw := range []string{stdout + "\n" + stderr, stderr + "\n" + stdout} {
		var reply nativeCrewKeeperReply
		if err := decodeNativeKeeperCrewReply([]byte(raw), 1, &reply); err != nil {
			t.Fatalf("separate CLI result and diagnostic lost in merged stream: %v", err)
		}
		if reply.RequestID != "fixture-request" || reply.Decision != "ALLOW" {
			t.Fatal("lost result identity")
		}
	}
}

func TestNativeKeeperCrewReplyRejectsAmbiguousStreams(t *testing.T) {
	allow := `{"request_id":"fixture-request","decision":"ALLOW","exit_code":1}`
	errorOne := `{"error":{"message":"fixture error","exit_code":1}}`
	for _, tc := range []struct {
		name, raw string
		code      int
	}{
		{"missing decision", errorOne, 1},
		{"duplicate decision", allow + allow + errorOne, 1},
		{"missing diagnostic", allow, 1},
		{"duplicate diagnostic", allow + errorOne + errorOne, 1},
		{"success with error", allow + errorOne, 0},
		{"tool exit mismatch", `{"request_id":"fixture-request","decision":"ALLOW","exit_code":2}` + errorOne, 1},
		{"diagnostic exit mismatch", allow + `{"error":{"message":"fixture","exit_code":2}}`, 1},
		{"unrecognized trailer", allow + errorOne + `{}`, 1},
		{"invalid trailing bytes", allow + errorOne + `not-json`, 1},
		{"missing identity", `{"decision":"ALLOW","exit_code":1}` + errorOne, 1},
		{"conflicting fields", `{"request_id":"fixture-request","decision":"ALLOW","error":{}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reply nativeCrewKeeperReply
			if err := decodeNativeKeeperCrewReply([]byte(tc.raw), tc.code, &reply); err == nil {
				t.Fatal("contradictory execution evidence accepted")
			}
		})
	}
}
