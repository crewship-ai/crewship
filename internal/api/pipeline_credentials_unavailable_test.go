package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestCredentialGate_UnavailableStoreBlocksHTTPAndNestedRuns(t *testing.T) {
	for _, closed := range []bool{false, true} {
		name := "nil DB"
		if closed {
			name = "closed DB"
		}
		t.Run(name, func(t *testing.T) {
			h := NewPipelineHandler(nil, slog.Default(), nil, nil)
			if closed {
				h.db = setupTestDB(t)
				if err := h.db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			dsl := &pipeline.DSL{CredsRequired: []pipeline.CredReq{{Type: "stripe"}}}
			rr := httptest.NewRecorder()
			if !h.gateMissingCredentials(rr, httptest.NewRequest("POST", "/run", nil), "ws", "crew", "Crew", dsl) {
				t.Fatal("unavailable store allowed run")
			}
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			err := h.RunPreflight().Check(context.Background(), pipeline.PreflightRequest{WorkspaceID: "ws", AuthorCrewID: "crew", PipelineSlug: "child", DSL: dsl})
			if !errors.Is(err, pipeline.ErrRunPreflightBlocked) {
				t.Fatalf("nested run allowed or wrong refusal: %v", err)
			}
			rr = httptest.NewRecorder()
			if h.gateMissingCredentials(rr, httptest.NewRequest("POST", "/run", nil), "ws", "crew", "Crew", &pipeline.DSL{}) {
				t.Fatal("no-requirement fast path was blocked")
			}
		})
	}
}
