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
		// Windows paths carry backslashes that are NOT escape sequences. An
		// unquoted backslash only escapes when followed by space, tab, quote,
		// or another backslash — otherwise it is a literal character, and the
		// same narrowed rule applies inside double quotes (escapes only `"`
		// and `\`). See issue #1140.
		{"unquoted windows path survives", `C:\npx.exe`, []string{`C:\npx.exe`}},
		{"quoted windows path with spaces survives", `"C:\Program Files\nodejs\npx.exe"`, []string{`C:\Program Files\nodejs\npx.exe`}},
		{"escaped quote inside double quotes", `"say \"hi\""`, []string{`say "hi"`}},
		{"trailing lone backslash is literal", `a\`, []string{`a\`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Fields(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Fields(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
