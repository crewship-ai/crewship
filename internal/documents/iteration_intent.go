package documents

import "errors"

// IterationIntent identifies one explicit existing Document, never a slug,
// principal or mode supplied by a model. The host binds its human chat receipt.
type IterationIntent struct {
	SubmissionID string `json:"submission_id"`
	PageID       string `json:"page_id"`
	Schema       int    `json:"schema"`
	Rev          int64  `json:"rev"`
	Generation   int64  `json:"generation"`
}

func (v IterationIntent) Validate() error {
	if !creationSubmissionID.MatchString(v.SubmissionID) || !creationSubmissionID.MatchString(v.PageID) || v.Schema != 3 || v.Rev < 0 || v.Generation < 0 {
		return errors.New("one explicit Document identity and original schema/revision/generation required")
	}
	return nil
}
