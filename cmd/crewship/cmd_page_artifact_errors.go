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

// artifactStorageQuotaCode is sent with 413 when the workspace's (or the
// instance's) aggregate artifact storage is full, not just this artifact's.
const artifactStorageQuotaCode = "artifact_storage_quota"

// artifactQuotaError keeps the server's APIError in the chain, so a
// --format json error envelope still carries status, detail and the code,
// while the human message says what a CLI caller needs to know: nothing was
// written and the local input is still theirs.
type artifactQuotaError struct {
	api     *cli.APIError
	storage bool
}

func (e *artifactQuotaError) Error() string {
	if e.storage {
		return "workspace artifact storage is full: nothing was saved. Your local input (--file) is unchanged and every existing version is retained; history is never deleted automatically. " +
			"A workspace owner or admin can check usage with `crewship page artifact storage`, free space or raise capacity, then save again."
	}
	return "artifact history quota is full: nothing was saved. Your local input (--file) is unchanged and every existing version is retained. " +
		"Ask the operator for more storage, or save a copy under a new slug with `crewship page artifact create`."
}
func (e *artifactQuotaError) Unwrap() error { return e.api }
func (e *artifactQuotaError) ExitCode() int { return cli.ExitValidation }

// artifactQuotaCheck recognises the 413 artifact_history_quota and
// artifact_storage_quota refusals. For
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
	if json.Unmarshal(raw, &body) != nil || body.Code != artifactHistoryQuotaCode && body.Code != artifactStorageQuotaCode {
		return nil
	}
	err := cli.CheckError(&http.Response{StatusCode: resp.StatusCode, Body: io.NopCloser(bytes.NewReader(raw))})
	var api *cli.APIError
	if !errors.As(err, &api) {
		return err
	}
	return &artifactQuotaError{api: api, storage: body.Code == artifactStorageQuotaCode}
}
