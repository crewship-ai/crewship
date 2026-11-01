package api

import (
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pages"
)

// writePagesLimit renders the one refusal every page-creating path gives when
// the workspace is at its page cap: 422, kind "cap", code "pages_limit", the
// sentence stating the limit, and the numbers in detail.
func writePagesLimit(w http.ResponseWriter, e *pages.LimitError) {
	writeRejection(w, pageRejection{
		Kind:    "cap",
		Code:    "pages_limit",
		Message: e.Error(),
		Detail: map[string]any{
			"pages_existing": e.Existing,
			"pages_limit":    e.Limit,
		},
	})
}

// pagesLimitFrom reports whether err is the page-cap refusal.
func pagesLimitFrom(err error) (*pages.LimitError, bool) {
	var e *pages.LimitError
	return e, errors.As(err, &e)
}
