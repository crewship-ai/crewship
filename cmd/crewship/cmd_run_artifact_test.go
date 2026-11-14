package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactRefsMetadataShape(t *testing.T) {
	got, _ := json.Marshal(artifactRefsMetadata([]string{"a1", "b_2"}))
	if string(got) != `[{"page_id":"a1"},{"page_id":"b_2"}]` {
		t.Fatalf("shape = %s", got)
	}
}

func TestValidateArtifacts(t *testing.T) {
	ten := strings.Split(strings.Repeat("p,", 10), ",")[:10]
	eleven := append(append([]string{}, ten...), "q")
	tests := []struct {
		name        string
		f           runExtrasFlags
		interactive bool
		docTarget   bool
		wantErr     bool
	}{
		{"one", runExtrasFlags{artifacts: []string{"notes"}}, false, false, false},
		{"ten", runExtrasFlags{artifacts: ten}, false, false, false},
		{"eleven", runExtrasFlags{artifacts: eleven}, false, false, true},
		{"empty", runExtrasFlags{artifacts: []string{""}}, false, false, true},
		{"slash", runExtrasFlags{artifacts: []string{"a/b"}}, false, false, true},
		{"page", runExtrasFlags{artifacts: []string{"a"}, page: "b"}, false, false, true},
		{"create", runExtrasFlags{artifacts: []string{"a"}, createDocument: true}, false, false, true},
		{"askform", runExtrasFlags{artifacts: []string{"a"}, askForm: "f"}, false, false, true},
		{"interactive", runExtrasFlags{artifacts: []string{"a"}}, true, false, true},
		{"document target", runExtrasFlags{artifacts: []string{"a"}}, false, true, true},
	}
	for _, tc := range tests {
		err := tc.f.validate("hi", "chat", tc.interactive, tc.docTarget)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}
