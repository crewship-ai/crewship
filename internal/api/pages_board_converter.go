package api

import (
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pagebuild"
)

func (h *PageHandler) boardConverterURL() string {
	if h.pageRuntimeOrigin == "" || pagebuild.ValidateRuntimeOrigin(h.pageRuntimeOrigin, h.pageStudioOrigin) != nil {
		return ""
	}
	return strings.TrimSuffix(h.pageRuntimeOrigin, "/") + pagebuild.BoardConverterPath
}

// BoardConverter is public constant code, not a read path for Board content.
// Authenticated request APIs disclose availability and supply authorized input.
func (h *PageHandler) BoardConverter(w http.ResponseWriter, r *http.Request) {
	if h.boardConverterURL() == "" || !pagebuild.RuntimeMatchesHost(h.pageRuntimeOrigin, r.Host) {
		replyError(w, 404, "Board converter is not served on this host")
		return
	}
	pagebuild.ServeBoardConverter(w, h.pageRuntimeOrigin, h.pageStudioOrigin)
}
