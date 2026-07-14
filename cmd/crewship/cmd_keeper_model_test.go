package main

import (
	"strings"
	"testing"
)

// The `keeper model set` client-side validation runs BEFORE any auth/network
// (requireAuthAndWorkspace), so these guards are testable with no server.
func TestKeeperModelSet_Validation(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		wantErr  string
	}{
		{"empty provider", "", "m", "--provider must be one of"},
		{"unknown provider", "gemini", "m", "--provider must be one of"},
		{"missing model", "ollama", "", "--model is required"},
		{"overlong model", "ollama", strings.Repeat("x", 201), "maximum"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keeperModelSetProvider = tt.provider
			keeperModelSetModel = tt.model
			keeperModelSetCredential = ""
			err := keeperModelSetCmd.RunE(keeperModelSetCmd, nil)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
