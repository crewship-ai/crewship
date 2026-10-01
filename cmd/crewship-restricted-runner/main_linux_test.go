package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCollectResponses(t *testing.T) {
	tests := []struct {
		name, input, want string
		fail              bool
	}{
		{"text", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "hello", false},
		{"disconnect", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", "", true},
		{"failure", "data: {\"type\":\"response.failed\"}\n\n", "", true},
		{"late", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"late\"}\n\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := collectResponses(strings.NewReader(tt.input), &out)
			if (err != nil) != tt.fail {
				t.Fatalf("error %v", err)
			}
			if !tt.fail && (!strings.Contains(out.String(), tt.want) || !strings.Contains(out.String(), `"type":"done"`)) {
				t.Fatalf("output %q", out.String())
			}
		})
	}
}
