package api

import (
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func (h *PipelineHandler) FixtureTest(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	var body pipeline.FixtureStepInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid fixture test body")
		return
	}
	result, err := pipeline.TestStepWithFixtures(body)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
