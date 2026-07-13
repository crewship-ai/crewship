package shlex

import (
	"reflect"
	"testing"
)

func TestFields(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "npx", []string{"npx"}},
		{"plain multi", "npx -y @scope/pkg", []string{"npx", "-y", "@scope/pkg"}},
		{"collapses runs", "  a\t b \n c ", []string{"a", "b", "c"}},
		{"double quotes group spaces", `npx -y "@scope/pkg with space"`, []string{"npx", "-y", "@scope/pkg with space"}},
		{"single quotes group spaces", `sh -c 'echo hi there'`, []string{"sh", "-c", "echo hi there"}},
		{"quoted spaced path", `"/opt/my app/bin/server"`, []string{"/opt/my app/bin/server"}},
		{"adjacent quote concatenation", `a"b c"d`, []string{"ab cd"}},
		{"empty quoted field preserved", `cmd "" x`, []string{"cmd", "", "x"}},
		{"backslash escapes space", `a\ b`, []string{"a b"}},
		{"backslash literal inside single quotes", `'a\b'`, []string{`a\b`}},
		{"unterminated quote consumes rest", `cmd "unclosed`, []string{"cmd", "unclosed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Fields(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Fields(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
