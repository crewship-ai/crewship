package backup

import "testing"

func TestIssueOutboxExactJSONRejectsTrailingDataAndPreservesNumbers(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{`{"counter":9007199254740993}`, `{"counter":9007199254740992}`, false},
		{`{"a":1} trailing-garbage`, `{"a":1}`, false},
		{`{"a":1}`, `{"a":1} {"b":2}`, false},
		{`{"counter":9007199254740993,"a":1}`, " {\"a\":1,\"counter\":9007199254740993} \n", true},
	} {
		if got := sameOutboxJSON([]byte(tc.a), []byte(tc.b)); got != tc.equal {
			t.Fatalf("exact JSON %q vs %q: got=%v expected=%v", tc.a, tc.b, got, tc.equal)
		}
	}
}
