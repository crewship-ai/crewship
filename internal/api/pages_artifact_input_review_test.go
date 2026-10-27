package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestArtifactProjectHTTPRequiresNonNullFileStrings(t *testing.T) {
	_, _, owner, call := artifactProjectFixture(t)
	for i, file := range []string{
		`{"path":"index.html","content":null,"media_type":"text/html"}`,
		`{"path":"index.html","media_type":"text/html"}`,
		`{"path":"index.html","content":"","media_type":null}`,
	} {
		body := fmt.Sprintf(`{"slug":"invalid-%d","name":"Invalid","entrypoint":"index.html","files":[%s]}`, i, file)
		if w := call(owner, "POST", "artifacts", body); w.Code != 400 {
			t.Errorf("invalid file accepted: %d %s", w.Code, w.Body)
		}
	}
	if w := call(owner, "POST", "artifacts", `{"slug":"empty-content","name":"Empty file","entrypoint":"index.html","files":[{"path":"index.html","content":"","media_type":"text/html"}]}`); w.Code != 201 {
		t.Fatal("valid empty string rejected", w.Code, w.Body)
	}
}

func TestArtifactProjectHTTPJSONDepthBounded(t *testing.T) {
	var out map[string]json.RawMessage
	deep := `{"payload":` + strings.Repeat("[", 256) + `0` + strings.Repeat("]", 256) + `}`
	if err := decodeArtifactProjectJSONBytes([]byte(deep), &out); err == nil {
		t.Fatal("deep recursive transport accepted")
	}
	shallow := `{"payload":[[0]]}`
	if err := decodeArtifactProjectJSONBytes([]byte(shallow), &out); err != nil {
		t.Fatal("normal transport rejected", err)
	}
}
