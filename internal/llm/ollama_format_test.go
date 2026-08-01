package llm

import (
	"encoding/json"
	"testing"
)

// Structured outputs are the third way a correctly configured Keeper judge could
// deny everything while looking healthy.
//
// The judge asks for JSON in prose ("Respond with ONLY valid JSON, no other
// text") and then brace-scans the answer. A model that prefixes "Sure, here's my
// assessment:" or wraps the object in a code fence produces nothing the parser
// accepts, and NormalizeRawResponse turns an unparseable answer into DENY risk
// 10 — fail-closed, by design. So a chatty small model refuses every credential
// request without a single error being logged.
//
// Ollama takes a top-level "format" field carrying a JSON schema and constrains
// decoding to it, which removes that failure mode structurally rather than
// asking the model more firmly. Request.Format carries it under the same
// discipline as Request.Think: nil omits the key entirely, so a provider or a
// model server that does not implement it sees exactly the request it saw
// before.

// TestOllamaBuildRequestBody_FormatOmittedUnlessSet pins the default. Sending a
// schema unconditionally would push it at every model on the instance, including
// the ones served by an Ollama old enough to reject an unknown field — and the
// Keeper judge is not the only caller of this provider.
func TestOllamaBuildRequestBody_FormatOmittedUnlessSet(t *testing.T) {
	t.Parallel()
	body, err := NewOllama("http://x", "m").buildRequestBody(Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["format"]; present {
		t.Errorf("format = %v, want the key absent when the caller did not ask", got["format"])
	}
}

// TestOllamaBuildRequestBody_FormatIsTopLevel is the fix the judge depends on.
// "format" is a top-level Ollama field; inside "options" it is accepted and
// silently ignored, which looks exactly like a model that cannot follow a schema.
func TestOllamaBuildRequestBody_FormatIsTopLevel(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"decision": map[string]any{"type": "string", "enum": []string{"ALLOW", "DENY", "ESCALATE"}},
		},
		"required": []string{"decision"},
	}
	body, err := NewOllama("http://x", "m").buildRequestBody(Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Format:   schema,
	}, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	format, present := got["format"]
	if !present {
		t.Fatal("format key missing — decoding is unconstrained and a chatty model still DENYs every request")
	}
	obj, ok := format.(map[string]any)
	if !ok {
		t.Fatalf("format = %T, want the schema object", format)
	}
	if obj["type"] != "object" {
		t.Errorf("format.type = %v, want the schema to survive marshalling intact", obj["type"])
	}
	if opts, ok := got["options"].(map[string]any); ok {
		if _, leaked := opts["format"]; leaked {
			t.Error("format landed in options, where Ollama ignores it")
		}
	}
}

// TestOllamaBuildRequestBody_FormatStringMode covers the other shape Ollama
// accepts: the bare "json" mode, which is what a caller with no schema to hand
// would reach for. The field is `any` precisely so both work without a second
// field to keep in sync.
func TestOllamaBuildRequestBody_FormatStringMode(t *testing.T) {
	t.Parallel()
	body, err := NewOllama("http://x", "m").buildRequestBody(Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Format:   "json",
	}, true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["format"] != "json" {
		t.Errorf("format = %v, want %q", got["format"], "json")
	}
}
