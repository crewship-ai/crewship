package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jsonBodyOfSize marshals base (with an added "_pad" filler key) to exactly
// targetLen bytes of JSON. "_pad" is a key no handler under test reads —
// encoding/json ignores unrecognized fields by default — so a request
// built this way is otherwise IDENTICAL to one that would succeed; only
// its size differs. That is what makes the oversized case below a real
// proof of the byte cap rather than a coincidental 400 from a malformed
// or incomplete body.
func jsonBodyOfSize(t *testing.T, base map[string]any, targetLen int) []byte {
	t.Helper()
	withPad := make(map[string]any, len(base)+1)
	for k, v := range base {
		withPad[k] = v
	}
	withPad["_pad"] = ""
	empty, err := json.Marshal(withPad)
	if err != nil {
		t.Fatalf("marshal (empty pad): %v", err)
	}
	need := targetLen - len(empty)
	if need < 0 {
		t.Fatalf("base body (%d bytes) already exceeds targetLen (%d)", len(empty), targetLen)
	}
	withPad["_pad"] = strings.Repeat("a", need)
	out, err := json.Marshal(withPad)
	if err != nil {
		t.Fatalf("marshal (padded): %v", err)
	}
	if len(out) != targetLen {
		t.Fatalf("padded body = %d bytes, want exactly %d", len(out), targetLen)
	}
	return out
}

// TestPipelineWrite_BodySizeCap pins #1416 item 4: Save, ImportPipeline,
// CreateSchedule, and CreateWebhook must cap their request body the same
// way every exec route already does (maxExecBodyBytes via
// http.MaxBytesReader) — before this fix, json.NewDecoder(r.Body) had no
// upper bound, so a create-role member (every one of these routes only
// requires "create"/MANAGER+) could pin server memory with an oversized
// body.
//
// Each subtest sends the SAME otherwise-valid, otherwise-successful
// request twice — once at maxExecBodyBytes-1 (must still succeed: the cap
// must not clip a legitimate request at the boundary) and once at
// maxExecBodyBytes+1 (must now be rejected). Padding an unrecognized
// "_pad" JSON key (silently ignored by encoding/json) is what makes the
// two requests otherwise byte-for-byte equivalent in every way that
// matters to the handler — isolating size as the only variable and ruling
// out a false-pass from a coincidental 400 on some other validation path.
func TestPipelineWrite_BodySizeCap(t *testing.T) {
	t.Run("Save", func(t *testing.T) {
		h, userID, wsID, crewID := covPCHandler(t)
		base := map[string]any{
			"slug":           "pad-save",
			"name":           "pad-save name",
			"description":    "desc",
			"definition":     json.RawMessage(covPCDef("pad-save")),
			"author_crew_id": crewID,
			"skip_test_gate": true,
		}

		t.Run("just under the cap still succeeds", func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", bytes.NewReader(jsonBodyOfSize(t, base, maxExecBodyBytes-1)))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Save(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
		t.Run("just over the cap is rejected", func(t *testing.T) {
			base["slug"] = "pad-save-2" // fresh slug: the first subtest already saved one
			req := httptest.NewRequest("POST", "/x", bytes.NewReader(jsonBodyOfSize(t, base, maxExecBodyBytes+1)))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Save(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (over the byte cap); body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
	})

	t.Run("ImportPipeline", func(t *testing.T) {
		h, userID, wsID, crewID := covPCHandler(t)
		mkBase := func(name string) map[string]any {
			return map[string]any{
				"format": "crewship-pipeline-bundle/v1",
				"pipeline": map[string]any{
					"name": name,
					"definition": json.RawMessage(`{"name":"` + name + `","steps":[` +
						`{"id":"t","type":"transform","transform":{"input":"x","expression":"."}}` +
						`]}`),
				},
				"author_crew_id": crewID,
			}
		}

		t.Run("just under the cap still succeeds", func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", bytes.NewReader(jsonBodyOfSize(t, mkBase("pad-import"), maxExecBodyBytes-1)))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ImportPipeline(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
		t.Run("just over the cap is rejected", func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", bytes.NewReader(jsonBodyOfSize(t, mkBase("pad-import-2"), maxExecBodyBytes+1)))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ImportPipeline(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (over the byte cap); body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
	})

	t.Run("CreateSchedule", func(t *testing.T) {
		h, db, userID, wsID := scheduleHandlerRig(t)
		seedPipelineRow(t, db, wsID, "pln_pad", "pad-target")
		base := map[string]any{
			"cron_expr":            "*/5 * * * *",
			"target_pipeline_slug": "pad-target",
		}

		t.Run("just under the cap still succeeds", func(t *testing.T) {
			req := withWorkspaceUser(httptest.NewRequest("POST",
				"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
				bytes.NewReader(jsonBodyOfSize(t, base, maxExecBodyBytes-1))),
				userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.CreateSchedule(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
		t.Run("just over the cap is rejected", func(t *testing.T) {
			req := withWorkspaceUser(httptest.NewRequest("POST",
				"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
				bytes.NewReader(jsonBodyOfSize(t, base, maxExecBodyBytes+1))),
				userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.CreateSchedule(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (over the byte cap); body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
	})

	t.Run("CreateWebhook", func(t *testing.T) {
		setTestEncryptionKeyParallelSafe(t)
		h, db, userID, wsID := webhookHandlerRig(t)
		seedWebhookPipeline(t, db, wsID, "pln_pad_wh", "pad-target-wh")
		base := map[string]any{
			"target_pipeline_slug": "pad-target-wh",
		}

		t.Run("just under the cap still succeeds", func(t *testing.T) {
			req := withWorkspaceUser(httptest.NewRequest("POST",
				"/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
				bytes.NewReader(jsonBodyOfSize(t, base, maxExecBodyBytes-1))),
				userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.CreateWebhook(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
		t.Run("just over the cap is rejected", func(t *testing.T) {
			req := withWorkspaceUser(httptest.NewRequest("POST",
				"/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
				bytes.NewReader(jsonBodyOfSize(t, base, maxExecBodyBytes+1))),
				userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.CreateWebhook(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (over the byte cap); body=%s", rr.Code, truncate(rr.Body.String(), 300))
			}
		})
	})
}
