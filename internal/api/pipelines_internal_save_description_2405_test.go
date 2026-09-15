package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #2405 — the agent-authored InternalSave path must treat description the
// way the user path does since #2373: an omitted field preserves the stored
// description, an explicit "" clears it, a value replaces it. Before this
// fix internalSaveRequest carried a plain string, so an agent re-emitting a
// routine without a description silently erased the one a human wrote.

// internalSaveBody2405 builds a save body for wsID/crewID with a valid
// save_token. descField is spliced in verbatim (including the leading
// comma) so a test can omit the key entirely, not just send "".
func internalSaveBody2405(wsID, crewID, descField string) string {
	token := signInternalSaveTokenForTest([]byte(testSaveTokenSecret1371), wsID, crewID, def1371)
	return `{"workspace_id":"` + wsID + `","slug":"my-pipe","name":"My Pipe",` +
		`"author_crew_id":"` + crewID + `",` +
		`"save_token":"` + token + `"` + descField + `,` +
		`"definition":` + def1371 + `}`
}

func internalSave2405(t *testing.T, h *PipelineHandler, body string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
}

func storedDescription2405(t *testing.T, h *PipelineHandler, wsID, slug string) string {
	t.Helper()
	var desc string
	if err := h.db.QueryRow(
		"SELECT COALESCE(description, '') FROM pipelines WHERE workspace_id = ? AND slug = ?", wsID, slug,
	).Scan(&desc); err != nil {
		t.Fatalf("read description: %v", err)
	}
	return desc
}

func TestPipelineInternalSave_DescriptionPatchSemantics_2405(t *testing.T) {
	cases := []struct {
		name      string
		descField string // spliced into the re-save body; "" omits the key
		want      string
	}{
		{name: "omitted preserves", descField: ``, want: "Written by a human"},
		{name: "explicit empty clears", descField: `,"description":""`, want: ""},
		{name: "value replaces", descField: `,"description":"Rewritten by the agent"`, want: "Rewritten by the agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, wsID, crewID := buildInternalSaveHandler1371(t)

			// First save lands the description a human would have written.
			internalSave2405(t, h, internalSaveBody2405(wsID, crewID, `,"description":"Written by a human"`))
			if got := storedDescription2405(t, h, wsID, "my-pipe"); got != "Written by a human" {
				t.Fatalf("seed description=%q", got)
			}

			// Re-save through the same door with the case's description field.
			internalSave2405(t, h, internalSaveBody2405(wsID, crewID, tc.descField))
			if got := storedDescription2405(t, h, wsID, "my-pipe"); got != tc.want {
				t.Errorf("description after re-save=%q want %q", got, tc.want)
			}
		})
	}
}
