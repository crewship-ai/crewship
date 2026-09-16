package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Validate receipts against oneOf itself: checking only property names misses
// overlapping branches and wrongly rejects every successful receipt.
func TestWebhookReceiptSchema_AcceptsExactlyOneBranch(t *testing.T) {
	_, schemas := finalIntegrationsConnectorsSchemaCatalog()
	raw, err := json.Marshal(schemas["FinalWebhookFire"])
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("receipt.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"accepted", `{"delivery_id":"d","work_id":"w","status":"queued","duplicate":false}`, true},
		{"duplicate", `{"delivery_id":"d","work_id":"w","status":"queued","duplicate":true}`, true},
		{"ignored", `{"delivery_id":"d","status":"ignored","reason":"filter"}`, true},
		{"missing work", `{"delivery_id":"d","status":"queued","duplicate":false}`, false},
		{"invalid ignored status", `{"delivery_id":"d","status":"queued"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.body), &value); err != nil {
				t.Fatal(err)
			}
			err := schema.Validate(value)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
