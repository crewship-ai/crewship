package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// `--format quiet` is a list command's contract with the next command in the
// pipe: one id per line, nothing else. Every one of these commands shortened
// the id while BUILDING the rows the quiet renderer prints, so what came out
// was a prefix — and a prefix fed back into `token revoke` / `label delete` /
// `session revoke` answers 404. `run list` had the same bug and fixed it at its
// own call site; these are the commands that fix did not reach.
//
// The three cover the three ways this repo shortens an id — truncateID (bare
// prefix), an inline slice, and truncateString (prefix + ellipsis) — so a
// regression in any of them is caught here rather than by a user's pipeline.
func TestListCommandsQuietEmitTheWholeID(t *testing.T) {
	// Long enough that all three widths (8, 12, 16) cut it.
	const fullID = "clabel0123456789abcdefghijklmnop"

	cases := []struct {
		name string
		path string
		body any
		run  func() error
	}{
		{
			name: "label list",
			path: "/api/v1/labels",
			body: []map[string]any{{"id": fullID, "name": "bug", "color": "#ff0000"}},
			run:  func() error { return labelListCmd.RunE(labelListCmd, nil) },
		},
		{
			name: "token list",
			path: "/api/v1/auth/cli-tokens",
			body: map[string]any{"data": []map[string]any{
				{"id": fullID, "name": "laptop", "created_at": "2026-08-01T10:00:00Z"},
			}},
			run: func() error { return tokenListCmd.RunE(tokenListCmd, nil) },
		},
		{
			name: "session list",
			path: "/api/v1/auth/sessions",
			body: []map[string]any{{
				"id": fullID, "created_at": "2026-08-01T10:00:00Z",
				"last_used_at": "2026-08-11T10:00:00Z", "user_agent": "crewship/1", "ip": "10.0.0.1",
			}},
			run: func() error { return sessionListCmd.RunE(sessionListCmd, nil) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := covSetupCli4(t)
			setFormatCov(t, "quiet")
			stub.OnGet(tc.path, clitest.JSONResponse(200, tc.body))

			out, err := covCaptureStdoutCli4(t, tc.run)
			if err != nil {
				t.Fatalf("RunE: %v", err)
			}
			if !strings.Contains(out, fullID) {
				t.Errorf("quiet output does not carry the whole id — a script cannot pipe it into the next command.\nwant: %s\ngot:\n%s", fullID, out)
			}
		})
	}
}

// …and the table still shortens: the column has a width to respect, and a
// 32-character id pushes every other column off a narrow terminal. Without
// this the "fix" would be to stop truncating everywhere, which is a different
// regression.
func TestListCommandsTableStillShortensTheID(t *testing.T) {
	const fullID = "clabel0123456789abcdefghijklmnop"

	stub := covSetupCli4(t)
	setFormatCov(t, "table")
	stub.OnGet("/api/v1/labels", clitest.JSONResponse(200,
		[]map[string]any{{"id": fullID, "name": "bug", "color": "#ff0000"}}))

	out, err := covCaptureStdoutCli4(t, func() error { return labelListCmd.RunE(labelListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, fullID) {
		t.Errorf("table column shows the untruncated id; got:\n%s", out)
	}
	if !strings.Contains(out, fullID[:12]) {
		t.Errorf("table lost the id prefix entirely; got:\n%s", out)
	}
}
