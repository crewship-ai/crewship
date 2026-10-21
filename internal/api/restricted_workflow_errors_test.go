package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

func TestRestrictedWorkflowErrorResponsesKeepGraphFailureOpaque(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"denied dependency", fmt.Errorf("graph: %w", restrictedworkflow.ErrDenied), http.StatusNotFound},
		{"unsupported declaration", fmt.Errorf("graph: %w", restrictedworkflow.ErrUnsupported), http.StatusBadRequest},
		{"storage unavailable", fmt.Errorf("private database detail: %w", context.DeadlineExceeded), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			replyRestrictedWorkflowError(response, tc.err)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private database detail") {
				t.Fatal("storage internals exposed")
			}
		})
	}
}
