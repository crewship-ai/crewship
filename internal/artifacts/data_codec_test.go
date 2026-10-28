package artifacts

import (
	"strings"
	"testing"
)

func TestDataBatchCodecRejectsAmbiguousCountersAndPreservesValues(t *testing.T) {
	valid := `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"n":9007199254740993}}]}`
	body, err := DecodeDataBatch([]byte(valid))
	if err != nil || string(body.Documents[0].Value) != `{"n":9007199254740993}` {
		t.Fatalf("exact value: %+v %v", body, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"ifVersion":0,`, "", 1),
		strings.Replace(valid, `"ifVersion":0`, `"ifVersion":0.5`, 1),
		strings.Replace(valid, `"ifVersion":0`, `"ifVersion":9007199254740993`, 1),
		strings.Replace(valid, `"n":9007199254740993`, `"n":1,"n":2`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":1,"updated_by":"spoof"`, 1),
		strings.Replace(valid, `"id":"row"`, `"id":"row","id":"other"`, 1),
		strings.Replace(valid, `"id":"row"`, `"id":"row","ID":"other"`, 1),
		strings.Replace(valid, `"ifVersion":0`, `"ifVersion":0,"IfVersion":1`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":1,"BASE_VERSION":2`, 1),
		strings.Replace(valid, `"documents":`, `"Documents":`, 1),
	} {
		if _, err := DecodeDataBatch([]byte(raw)); err == nil {
			t.Errorf("accepted ambiguous input: %s", raw)
		}
	}
}
