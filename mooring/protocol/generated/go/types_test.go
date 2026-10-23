package protocol

import (
	"encoding/json"
	"testing"
)

func TestCountersRemainStringsAndUnionRemainsObject(t *testing.T) {
	var change ChangeResult
	wire := `{"change_id":"c","status":"saved","operations":[{"op_id":"o","status":"saved","revision":"9007199254740993"}],"first_revision":"9007199254740993","last_revision":"9007199254740993"}`
	if err := json.Unmarshal([]byte(wire), &change); err != nil {
		t.Fatal(err)
	}
	if change.FirstRevision == nil || *change.FirstRevision != Counter("9007199254740993") {
		t.Fatal("counter precision lost")
	}
	var operation map[string]any
	if err := json.Unmarshal(change.Operations[0], &operation); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip map[string]any
	if err = json.Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if _, ok := roundtrip["operations"].([]any)[0].(map[string]any); !ok {
		t.Fatal("union encoded as base64 instead of object")
	}
}
