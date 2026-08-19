package orchestrator

import (
	"bytes"
	"testing"
)

// apiKeySource names WHERE the credential came from, not the credential — but
// it is an upstream field we do not control, it goes to disk on every run, and
// the whole point of logging it is that we cannot predict what it will say.
// So only values we recognise are logged verbatim; anything else logs as
// "other", which still answers "did the auth path change?" without putting an
// unknown string from the CLI into the log.
func TestSafeAPIKeySource(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"none", "none"},
		{"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"},
		{"apiKeyHelper", "apiKeyHelper"},
		{"temporary", "temporary"},
		// The case this exists for: anything unrecognised is reported as
		// having changed, without quoting it.
		{"sk-ant-api03-Zm9vYmFyYmF6", "other"},
		{"/login managed key from user@example.com", "other"},
	}
	for _, tc := range cases {
		if got := safeAPIKeySource(tc.in); got != tc.want {
			t.Errorf("safeAPIKeySource(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// End to end through the log line, because the sanitiser is only worth having
// if it sits between the field and the writer.
func TestLogResolvedModel_NeverLogsAnUnknownKeySourceVerbatim(t *testing.T) {
	var buf bytes.Buffer
	const secretish = "sk-ant-api03-Zm9vYmFyYmF6"
	logResolvedModel(captureSlog(&buf), "agent-1", "claude-haiku-4-5", "claude-haiku-4-5", "2.1.204", secretish)

	if bytes.Contains(buf.Bytes(), []byte(secretish)) {
		t.Fatalf("unrecognised apiKeySource reached the log verbatim: %s", buf.String())
	}
	rec := decodeLogLines(t, &buf)[0]
	if got := rec["api_key_source"]; got != "other" {
		t.Errorf("api_key_source = %v, want other", got)
	}
}
