package main

import "testing"

// TestShouldLoadSlashCommands locks the startup fast-path: every invocation
// used to pay a readdir + open/parse of every ~/.crewship/commands/*.md even
// for `crewship version`. When argv[1] is a built-in command, user-defined
// slash commands can never be dispatched, so the scan is skipped entirely.
func TestShouldLoadSlashCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		// Built-in commands can never be shadowed (collision policy: built-ins
		// win), so the scan is pure waste — skip it.
		{"built-in version", []string{"crewship", "version"}, false},
		{"built-in run", []string{"crewship", "run", "viktor", "hi"}, false},
		{"built-in agent", []string{"crewship", "agent", "list"}, false},

		// Anything that lists or could dispatch a slash command must load.
		{"bare crewship (help listing)", []string{"crewship"}, true},
		{"help", []string{"crewship", "help"}, true},
		{"--help flag", []string{"crewship", "--help"}, true},
		{"unknown = potential slash command", []string{"crewship", "my-review"}, true},
		{"completion machinery", []string{"crewship", "__complete", ""}, true},
		{"completion", []string{"crewship", "completion", "zsh"}, true},
		{"commands manifest dump", []string{"crewship", "commands"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldLoadSlashCommands(tt.args); got != tt.want {
				t.Errorf("shouldLoadSlashCommands(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
