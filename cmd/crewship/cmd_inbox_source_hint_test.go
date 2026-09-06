package main

// `inbox resolve` on a live waitpoint/escalation (#2413 item 5).
//
// The server's 409 is correct and deliberate — a live waitpoint has to be
// decided at its source or the decision never reaches the parked run — but the
// CLI relayed it verbatim:
//
//     409: use the source endpoint for this kind
//          (e.g. /pipelines/waitpoints/{token}/approve) — inbox PATCH only
//          supports 'read' for source-managed items
//
// which names an internal HTTP path an operator cannot run, while the token
// the real command needs was one GET away. `routine run` already prints the
// approve/reject pair when it parks on a waitpoint; these tests pin that the
// inbox now answers in the same shape.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// conflict409 builds the refusal the way inbox_handler.go writes it: the body
// echoes the kind, and nothing else.
func conflict409(kind string) clitest.Handler {
	return clitest.JSONResponse(409, map[string]any{
		"error": "use the source endpoint for this kind (e.g. /pipelines/waitpoints/{token}/approve) — " +
			"inbox PATCH only supports 'read' for source-managed items",
		"kind": kind,
	})
}

func TestInboxResolve_LiveWaitpointPrintsTheSourceCommands(t *testing.T) {
	s := covStubCli9(t)
	s.OnPatch("/api/v1/inbox/inb_1", conflict409("waitpoint"))
	s.OnGet("/api/v1/inbox/inb_1", clitest.JSONResponse(200, map[string]any{
		"id": "inb_1", "kind": "waitpoint", "source_id": "wp_tok_abc",
	}))

	err := patchInboxState("inb_1", "resolved", "approved")
	if err == nil {
		t.Fatal("expected the server's 409 to survive as an error")
	}
	msg := err.Error()
	// The original refusal must still be there — this decorates, it does not
	// replace, and a script matching on "API error (409)" keeps working.
	if !strings.Contains(msg, "409") {
		t.Errorf("the original 409 must survive:\n%s", msg)
	}
	if !strings.Contains(msg, "crewship routine waitpoints approve wp_tok_abc") {
		t.Errorf("missing the approve command with the real token:\n%s", msg)
	}
	if !strings.Contains(msg, "crewship routine waitpoints reject wp_tok_abc") {
		t.Errorf("missing the reject command:\n%s", msg)
	}
	if !strings.Contains(msg, "crewship inbox read inb_1") {
		t.Errorf("missing the 'just get it out of the feed' alternative:\n%s", msg)
	}
	if cli.ExitCodeFor(err) != cli.ExitConflict {
		t.Errorf("exit code = %d, want the conflict code — decorating must not reclassify the failure",
			cli.ExitCodeFor(err))
	}
}

func TestInboxResolve_LiveEscalationPointsAtEscalationResolve(t *testing.T) {
	s := covStubCli9(t)
	s.OnPatch("/api/v1/inbox/inb_2", conflict409("escalation"))
	s.OnGet("/api/v1/inbox/inb_2", clitest.JSONResponse(200, map[string]any{
		"id": "inb_2", "kind": "escalation", "source_id": "esc_9",
	}))

	err := patchInboxState("inb_2", "resolved", "approved")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "crewship escalation resolve esc_9 --action approve") {
		t.Errorf("missing the escalation source command:\n%s", err.Error())
	}
}

func TestInboxResolve_HintDegradesWhenTheRowCannotBeRead(t *testing.T) {
	// The token lookup is best-effort: a 409 whose row cannot be fetched must
	// still say what to do, with a placeholder where the token would go,
	// rather than turning the conflict into a lookup error.
	s := covStubCli9(t)
	s.OnPatch("/api/v1/inbox/inb_3", conflict409("waitpoint"))
	s.OnGet("/api/v1/inbox/inb_3", clitest.JSONResponse(500, map[string]any{"error": "boom"}))

	err := patchInboxState("inb_3", "resolved", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "crewship routine waitpoints approve <token>") {
		t.Errorf("expected a placeholder token, got:\n%s", err.Error())
	}
}

func TestInboxResolve_OtherErrorsAreUntouched(t *testing.T) {
	// A 404 (or any non-409) must pass through exactly as before — a hint
	// about waitpoints on an unrelated failure is noise that hides the real
	// message.
	s := covStubCli9(t)
	s.OnPatch("/api/v1/inbox/inb_4", clitest.JSONResponse(404, map[string]any{"error": "inbox item not found"}))

	err := patchInboxState("inb_4", "resolved", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "waitpoints approve") {
		t.Errorf("a 404 must not grow a waitpoint hint:\n%s", err.Error())
	}
}
