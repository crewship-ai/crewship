package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Parser DATA only, with a synthetic connection. No admission, original-source
// seal, native semantics, read/write authority or Saved result is created here.
func TestMooringOriginalHumanPayloadClosedAndSourceBound(t *testing.T) {
	c := &collaborationHTTPConnection{origin: "9007199254740991", admission: collaborationHumanAdmission{participant: "original-human"}}
	valid := `{"kind":"humancrdt.update","operation_id":"original-op","origin":"9007199254740991","update":"AQI=","authored_frontier":"Aw==","targets":["p"],"authored_checkpoint_state_digest":"sha256:` + strings.Repeat("a", 64) + `"}`
	for _, mode := range []string{"valid", "foreign-origin", "number-origin", "overflow", "leading-zero", "wrong-operation", "unknown-key", "case-key", "duplicate-key", "missing", "null-targets", "null-update", "null-frontier", "duplicate-target", "bad-update-padbits", "base64-newline", "digest-case", "foreign-participant", "task", "multiop"} {
		t.Run(mode, func(t *testing.T) {
			raw := valid
			switch mode {
			case "foreign-origin":
				raw = strings.Replace(raw, "9007199254740991", "7", 1)
			case "number-origin":
				raw = strings.Replace(raw, `"origin":"9007199254740991"`, `"origin":9007199254740991`, 1)
			case "overflow":
				raw = strings.Replace(raw, "9007199254740991", "9007199254740992", 1)
			case "leading-zero":
				raw = strings.Replace(raw, "9007199254740991", "09007199254740991", 1)
			case "wrong-operation":
				raw = strings.Replace(raw, `"operation_id":"original-op"`, `"operation_id":"foreign-op"`, 1)
			case "unknown-key":
				raw = strings.TrimSuffix(raw, "}") + `,"permission":true}`
			case "case-key":
				raw = strings.Replace(raw, `"kind"`, `"Kind"`, 1)
			case "duplicate-key":
				raw = strings.TrimSuffix(raw, "}") + `,"origin":"7"}`
			case "missing":
				raw = strings.Replace(raw, `"targets":["p"],`, "", 1)
			case "null-targets":
				raw = strings.Replace(raw, `["p"]`, "null", 1)
			case "null-update":
				raw = strings.Replace(raw, `"update":"AQI="`, `"update":null`, 1)
			case "null-frontier":
				raw = strings.Replace(raw, `"authored_frontier":"Aw=="`, `"authored_frontier":null`, 1)
			case "duplicate-target":
				raw = strings.Replace(raw, `["p"]`, `["p","p"]`, 1)
			case "bad-update-padbits":
				raw = strings.Replace(raw, "AQI=", "AQJ=", 1)
			case "base64-newline":
				raw = strings.Replace(raw, "AQI=", `AQI=\n`, 1)
			case "digest-case":
				raw = strings.Replace(raw, "sha256:a", "sha256:A", 1)
			}
			change := mooring.BatchChange{ID: "original-change", Operations: []mooring.BatchOperation{{ID: "original-op", Participant: "original-human", Payload: json.RawMessage(raw)}}}
			if mode == "foreign-participant" {
				change.Operations[0].Participant = "other-human"
			}
			if mode == "task" {
				change.TaskID = "agent-task"
			}
			if mode == "multiop" {
				change.Operations = append(change.Operations, change.Operations[0])
			}
			before := append([]byte(nil), change.Operations[0].Payload...)
			payload, err := c.parseOriginalHumanChange(change)
			if mode == "valid" {
				if err != nil || payload.Origin != c.origin || payload.Update != "AQI=" {
					t.Fatal(payload, err)
				}
			} else if err == nil {
				t.Fatal("invalid original payload accepted")
			}
			if !bytes.Equal(before, change.Operations[0].Payload) {
				t.Fatal("original immutable payload rewritten")
			}
		})
	}
}
