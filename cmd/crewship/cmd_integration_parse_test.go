package main

import (
	"reflect"
	"testing"
)

// parseStdioCommand must be shell-quote aware: quoted arguments that contain
// spaces (and bare executables at spaced paths) have to survive the split into
// command + args, not get shredded on whitespace. See issue #1132.
func TestParseStdioCommand(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantBin string
		wantArg []string
	}{
		{"empty", "", "", nil},
		{"bare command", "npx", "npx", nil},
		{"common npx", "npx -y @scope/pkg", "npx", []string{"-y", "@scope/pkg"}},
		{"common uvx", "uvx some-pkg", "uvx", []string{"some-pkg"}},
		{"double-quoted arg with space", `npx -y "@scope/pkg with space"`, "npx", []string{"-y", "@scope/pkg with space"}},
		{"single-quoted arg with space", `sh -c 'echo hello world'`, "sh", []string{"-c", "echo hello world"}},
		{"quoted spaced executable path", `"/opt/my app/bin/server"`, "/opt/my app/bin/server", nil},
		{"quoted spaced path with flag", `"/opt/my app/bin/server" --port 9`, "/opt/my app/bin/server", []string{"--port", "9"}},
		{"leading and trailing whitespace", "  npx  -y  pkg  ", "npx", []string{"-y", "pkg"}},
		{"empty quoted arg preserved", `cmd "" x`, "cmd", []string{"", "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBin, gotArg := parseStdioCommand(tt.raw)
			if gotBin != tt.wantBin {
				t.Errorf("bin = %q, want %q", gotBin, tt.wantBin)
			}
			if !reflect.DeepEqual(gotArg, tt.wantArg) {
				t.Errorf("args = %#v, want %#v", gotArg, tt.wantArg)
			}
		})
	}
}
