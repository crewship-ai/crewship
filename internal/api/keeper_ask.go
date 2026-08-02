package api

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// HandleAsk is POST /api/v1/admin/keeper/ask — put a credential request to this
// instance's judge as an operator, and get the verdict it would give an agent.
//
// Why it exists. `keeper judge test` proves the judge answers, but it asks ONE
// hard-coded scenario: an L1 npm token for a CI bot. "How would this judge rule
// on an SSH key with a thin justification?" had no answer short of waiting for
// an agent to want one — and the only way to find out was to provoke an agent
// turn and hope the agent asked for what it was told to.
//
// It is also what makes a ground-truth corpus buildable on purpose. The eval
// harness scores candidate models against decisions a HUMAN ruled on, and those
// exist only where the Keeper escalated something a person then resolved. With
// submission reachable only from a container, assembling twenty varied cases to
// judge meant twenty agent turns, each of which might paraphrase the intent or
// decline to ask at all — which makes the corpus a measurement of the agent
// rather than of the judge.
//
// It delegates to HandleRequest: the same evaluation, the same audit row, the
// same inbox behaviour. A separate code path would eventually answer a different
// question from the one production asks, which is the failure this package has
// already had twice (the think flag, then the format flag).
//
// The workspace comes from the SESSION and overwrites whatever the body says.
// Without that, an admin in one workspace could file requests into another by
// editing a field — the body is agent-supplied on the internal route, and the
// internal token binding that constrains it there does not exist here.
func (h *KeeperHandler) HandleAsk(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace is required")
		return
	}

	var body keeperRequestBody
	if err := readJSON(r, &body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.WorkspaceID = wsID

	raw, err := json.Marshal(body)
	if err != nil {
		replyInternalError(w, h.logger, "keeper ask: re-encode request", err)
		return
	}

	// Re-entering through the real handler rather than lifting its body out is
	// deliberate: the value here is that the operator's question travels the
	// exact path an agent's does, including the parts nobody thinks about — the
	// tier floors, the audit write, the inbox item, the health record.
	inner := r.Clone(r.Context())
	inner.Body = http.NoBody
	inner.Body = readCloser(raw)
	inner.ContentLength = int64(len(raw))
	h.HandleRequest(w, inner)
}

// readCloser wraps a byte slice as a request body.
func readCloser(b []byte) *bodyReader { return &bodyReader{Reader: bytes.NewReader(b)} }

type bodyReader struct{ *bytes.Reader }

func (b *bodyReader) Close() error { return nil }
