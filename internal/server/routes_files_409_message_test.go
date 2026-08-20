package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The 409 an operator hits when overwriting a crew-owned file has to
// name a command that starts a container. The old text ended "start the
// crew and retry", and the obvious reading of that — `crewship crew
// provision <crew>` — does not start anything: provision builds an
// image, and on a cache hit it prints "provisioned" while the container
// stays stopped. Following the message therefore produced the same 409
// again, which is how a deploy spent an hour retrying and concluded the
// server was broken.
//
// The container is created lazily on the crew's first agent run. That
// is the fact the message has to carry, because nothing else in the CLI
// surface tells you.
func TestContainerSaveErrorResponse_SharedTreeNamesAWorkingCommand(t *testing.T) {
	status, msg := containerSaveErrorResponse(
		fmt.Errorf("%w: exec failed", errCrewContainerUnavailable), containerCrewSharedRoot)

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if !strings.Contains(msg, "crewship crew start") {
		t.Errorf("409 does not name a command that starts the container:\n%s", msg)
	}
	// And it has to say why the command people reach for first is not
	// the one — otherwise they reach for it again.
	if !strings.Contains(msg, "provision") {
		t.Errorf("409 does not rule out `crew provision`, the wrong turn it invites:\n%s", msg)
	}
	if !strings.Contains(msg, "owned by the crew runtime") {
		t.Errorf("409 lost the diagnosis:\n%s", msg)
	}
}

// The /output tree is reached from the chat composer, whose reader is a
// user with no CLI. Its wording is asserted by the API proxy tests and
// must stay put.
func TestContainerSaveErrorResponse_OutputTreeUnchanged(t *testing.T) {
	_, msg := containerSaveErrorResponse(
		fmt.Errorf("%w: exec failed", errCrewContainerUnavailable), containerOutputRoot)

	if !strings.Contains(msg, "start the crew and retry") {
		t.Errorf("the chat-facing message changed; proxy_attachments tests pin it:\n%s", msg)
	}
}

// Non-container failures keep their own statuses — the 409 is a
// diagnosis, not a catch-all.
func TestContainerSaveErrorResponse_OtherFailuresUnchanged(t *testing.T) {
	if status, _ := containerSaveErrorResponse(errCrewNotFound, containerCrewSharedRoot); status != http.StatusNotFound {
		t.Errorf("crew-not-found status = %d, want 404", status)
	}
	if status, _ := containerSaveErrorResponse(fmt.Errorf("disk full"), containerCrewSharedRoot); status != http.StatusInternalServerError {
		t.Errorf("unknown failure status = %d, want 500", status)
	}
}
