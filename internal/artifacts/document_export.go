package artifacts

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/documents"
)

// UnsupportedExportRequest and UnsupportedExportResult deliberately carry no
// format, content, credential or destination. They describe an unavailable
// operation slot, not a promised export DTO or executable export service.
type UnsupportedExportRequest struct{}
type UnsupportedExportResult struct{}

// Export fails before inspecting a key or invoking authority. Existing Document
// engine export paths remain separate and are not activated by this interface.
func (*DocumentAdapter) Export(context.Context, documents.Key, UnsupportedExportRequest, documents.Authorize) (UnsupportedExportResult, error) {
	return UnsupportedExportResult{}, fmt.Errorf("%w: generic Document export is unavailable", ErrUnsupported)
}
