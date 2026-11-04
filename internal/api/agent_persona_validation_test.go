package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPersona_EmptyPutAndUnwrittenReset(t *testing.T) {
	for _, layer := range []string{"agent", "crew"} {
		t.Run(layer, func(t *testing.T) {
			r := newPersonaTestRig(t)
			put := r.h.PutAgentPersona
			reset := r.h.DeleteAgentPersona
			path := filepath.Join(r.output, "crews", r.crewID, "agents", "alice", ".memory")
			if layer == "crew" {
				put = r.h.PutCrewPersona
				reset = r.h.DeleteCrewPersona
				path = filepath.Join(r.output, "crews", r.crewID, "shared", ".memory")
			}
			for _, body := range []map[string]string{{}, {"content": ""}, {"content": " \n\t"}} {
				rec := httptest.NewRecorder()
				put(rec, r.authedReq(t, http.MethodPut, "/", body))
				if rec.Code != http.StatusBadRequest {
					t.Errorf("PUT %v: status=%d want 400, body=%s", body, rec.Code, rec.Body.String())
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				rec := httptest.NewRecorder()
				reset(rec, r.authedReq(t, http.MethodDelete, "/", nil))
				if rec.Code != http.StatusNoContent {
					t.Errorf("unwritten reset %d: status=%d want 204, body=%s", attempt, rec.Code, rec.Body.String())
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("empty PUT/reset created memory directory: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("blocked storage"), 0600); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			reset(rec, r.authedReq(t, http.MethodDelete, "/", nil))
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("storage failure reset: status=%d want 500, body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
