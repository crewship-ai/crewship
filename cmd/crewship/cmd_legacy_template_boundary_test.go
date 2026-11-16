package main

import "testing"

// The legacy /templates API and /workflow-templates share a table but accept
// incompatible template_json shapes. Its GUI callers cannot be removed until
// migrated; a convenience CLI must not expose the weaker write contract.
func TestTemplateCommandDoesNotExposeLegacyTemplateWrites(t *testing.T) {
	for _, command := range templateCmd.Commands() {
		if command.Name() == "prompt" {
			t.Fatal("legacy template writes must not be exposed as prompt templates")
		}
	}
}
