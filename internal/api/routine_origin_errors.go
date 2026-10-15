package api

import (
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func writeRoutineOriginError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, runauthority.ErrUnavailable) || errors.Is(err, runauthority.ErrIdentityMismatch) || errors.Is(err, runauthority.ErrRequesterStale) {
		writeChatIssueError(w, err)
		return true
	}
	return false
}
