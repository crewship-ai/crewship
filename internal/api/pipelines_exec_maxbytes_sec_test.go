package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// pipelines_exec_maxbytes_sec_test.go — request-body size cap guard.
//
// The exec decode sites (Run / DryRun / TestRun / ApproveWaitpoint) feed
// json.NewDecoder(r.Body) with no upper bound, so a single oversized POST
// could pin memory while the decoder buffers it. This test wraps the body
// in http.MaxBytesReader (mirroring user_preferences.go) by asserting an
// oversized — but otherwise well-formed — payload is rejected at the decode
// boundary with 400 or 413.
//
// RED before the fix: a multi-MB valid-JSON body decodes fine and the
// handler proceeds past the decode (no 400/413). GREEN after: MaxBytesReader
// trips and Decode errors → 400/413.
//
// Reuses newPipelineHandlerForCRUDTest, seedPipelineWithVersions,
// withWorkspaceUser.
// ---------------------------------------------------------------------------

func TestSecPipeMax_DryRun_OversizedBody_Rejected(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pl_maxbytes", "maxbytes-pipe", 1)

	// Well-formed JSON whose "inputs" map carries a multi-MB string value.
	// Valid for the uncapped decoder (would sail past → not 400), but far
	// over any sane request cap once MaxBytesReader is in place.
	huge := strings.Repeat("A", 8*1024*1024)
	body := `{"inputs":{"blob":"` + huge + `"}}`

	req := httptest.NewRequest("POST", "/api/v1/pipelines/maxbytes-pipe/dry_run",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", "maxbytes-pipe")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()

	h.DryRun(rr, req)

	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (body %s), want 400 or 413 for an oversized request body",
			rr.Code, rr.Body.String())
	}
}
