package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// keeperPermissionHint was written for `system keeper`, where the server really
// does answer a bare RFC-7807 "Forbidden" and a MEMBER deserves to be told why.
// It then got applied to every keeper command — including `keeper resolve`,
// whose 403 is normally the FOUR-EYES rule and carries its own precise message.
//
// On dev2 that cost real time: resolving an escalation returned
//
//	API error (403): keeper status requires ADMIN or OWNER role in workspace "…"
//	— ask a workspace admin or switch workspaces
//
// to a caller who was already OWNER. The server had said "this escalation was
// raised by an agent you own, so somebody else must confirm it"; the CLI threw
// that away and invented a role problem. An operator following the printed
// advice would go ask an admin to fix a permission that was never wrong, when
// the actual answer is "get a second person".
//
// So: substitute only when the server offered nothing. A message the server
// took the trouble to write is always better than one the client guessed.

// Built as a literal: APIError's rendered message is unexported, and the two
// fields the hint reads — Status and Detail — are exactly the two it needs.
func hintFor(status int, detail string) (error, *cli.APIError) {
	in := &cli.APIError{Status: status, Detail: detail}
	return keeperPermissionHint(in), in
}

func TestKeeperPermissionHint_KeepsTheServersOwnReason(t *testing.T) {
	cases := []string{
		"critical credential tier requires a second approver: this escalation was raised by an agent you own, so somebody else must confirm it",
		"workspace policy requires a second approver: this escalation was raised by an agent you own, so somebody else must confirm it",
	}
	for _, detail := range cases {
		out, in := hintFor(403, detail)
		got := out.Error()
		if out == error(in) {
			got = in.Detail // passed through untouched, which is the pass
		}
		if !strings.Contains(got, "second approver") {
			t.Errorf("the four-eyes reason was replaced by a guess:\ngot  %s\nwant it to keep %q", got, detail)
		}
		if strings.Contains(got, "ask a workspace admin") {
			t.Errorf("the message sends the operator after a role problem that does not exist:\n%s", got)
		}
	}
}

// The original behaviour still has to hold where it was right: a bare
// "Forbidden" tells a MEMBER nothing, and that is the case the hint exists for.
func TestKeeperPermissionHint_StillExplainsABareForbidden(t *testing.T) {
	for _, detail := range []string{"", "Forbidden", "forbidden"} {
		out, _ := hintFor(403, detail)
		got := out.Error()
		if !strings.Contains(got, "ADMIN or OWNER") {
			t.Errorf("a bare %q was left unexplained: %s", detail, got)
		}
	}
}

// Non-403s pass through untouched — the status and detail are the answer.
func TestKeeperPermissionHint_LeavesOtherStatusesAlone(t *testing.T) {
	out, in := hintFor(409, "this request is already settled as DENY")
	if out != error(in) {
		t.Errorf("a 409 was rewritten as a permission problem: %s", out)
	}

	plain := errors.New("dial tcp: connection refused")
	if got := keeperPermissionHint(plain); got != plain {
		t.Errorf("a non-API error was altered: %v", got)
	}
}
