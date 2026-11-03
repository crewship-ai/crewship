package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/design"
)

// Both engine phase timeouts stay a retryable 503 with the stable message;
// the phase is for the server log, not the client.
func TestDesignErrorMapsEngineTimeoutsToUnavailable(t *testing.T) {
	for _, err := range []error{
		design.ErrUnavailable,
		&design.TimeoutError{Phase: design.PhaseStartup, Budget: design.DefaultStartupTimeout},
		&design.TimeoutError{Phase: design.PhaseWork, Budget: design.DefaultWorkTimeout},
	} {
		t.Run(err.Error(), func(t *testing.T) {
			w := httptest.NewRecorder()
			designError(w, err)
			if w.Code != 503 || !strings.Contains(w.Body.String(), "Design engine is unavailable; changes are not acknowledged") {
				t.Fatalf("designError(%v) = %d %s", err, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "budget") {
				t.Fatalf("phase detail leaked to the client: %s", w.Body.String())
			}
		})
	}
}
