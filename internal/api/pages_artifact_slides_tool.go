package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/artifacts/slidespdf"
)

// Export bytes and their JSON transport are produced inside the same processing
// callback: serveArtifactSlides checks current authority after both operations.
func (h *PageHandler) exportArtifactSlidesTool(w http.ResponseWriter, r *http.Request, version int64) {
	h.serveArtifactSlides(w, r, false, artifactSlidesPDFProcess(version))
}

func artifactSlidesPDFProcess(version int64) artifactSlidesProcess {
	return func(ctx context.Context, p artifacts.ArtifactSlidesProjection) ([]byte, error) {
		pdf, err := slidespdf.Render(ctx, p.Deck)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(pdf)
		return artifacts.MarshalDataJSON(artifacts.ArtifactSlidesPDFResponse{
			MediaType: "application/pdf", Encoding: "base64", CodeVersion: version,
			ByteLength: len(pdf), SHA256: hex.EncodeToString(sum[:]), Data: base64.StdEncoding.EncodeToString(pdf),
		})
	}
}

func (h *PageHandler) forwardArtifactSlidesTool(w http.ResponseWriter, r *http.Request, operation string, payload []byte) {
	var input struct {
		Version int64 `json:"version"`
	}
	if !strings.HasPrefix(r.PathValue("slug"), "@") || len(payload) > 1<<20 || artifacts.DecodeDataJSON(payload, &input, "version") != nil || input.Version < 1 || input.Version > artifactSlidesMaxVersion {
		replyError(w, http.StatusBadRequest, "invalid Slides read")
		return
	}
	q := r.URL.Query()
	q.Set("version", strconv.FormatInt(input.Version, 10))
	r.URL.RawQuery = q.Encode()
	r.Method = http.MethodGet
	if operation == "artifact_slides_read" {
		h.GetArtifactSlides(w, r)
	} else {
		h.exportArtifactSlidesTool(w, r, input.Version)
	}
}
