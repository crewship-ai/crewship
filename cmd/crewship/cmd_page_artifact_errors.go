package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/cli"
)

// artifactHistoryQuotaCode is the machine code the artifact project API sends
// with HTTP 413 when the retained version history is full
// (internal/api/pages_artifact_project.go, artifactProjectError).
const artifactHistoryQuotaCode = "artifact_history_quota"

// artifactQuotaError keeps the server's APIError in the chain, so a
// --format json error envelope still carries status, detail and the code,
// while the human message says what a CLI caller needs to know: nothing was
// written and the local input is still theirs.
type artifactQuotaError struct{ api *cli.APIError }

func (e *artifactQuotaError) Error() string {
	return "artifact history quota is full: nothing was saved. Your local input (--file) is unchanged and every existing version is retained. " +
		"Ask the operator for more storage, or save a copy under a new slug with `crewship page artifact create`."
}
func (e *artifactQuotaError) Unwrap() error { return e.api }
func (e *artifactQuotaError) ExitCode() int { return cli.ExitValidation }

// artifactQuotaCheck recognises the 413 artifact_history_quota refusal. For
// any other response it restores the body and returns nil so the caller's
// ordinary error rendering runs unchanged.
func artifactQuotaCheck(resp *http.Response) error {
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		Code string `json:"code" yaml:"code"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Code != artifactHistoryQuotaCode {
		return nil
	}
	err := cli.CheckError(&http.Response{StatusCode: resp.StatusCode, Body: io.NopCloser(bytes.NewReader(raw))})
	var api *cli.APIError
	if !errors.As(err, &api) {
		return err
	}
	return &artifactQuotaError{api: api}
}
