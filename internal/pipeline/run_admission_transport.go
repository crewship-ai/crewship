package pipeline

import "bytes"

// CheckRunAdmissionPayload validates only the existing private canonical codec
// and its outer storage tuple. It performs no SQL, source authentication,
// execution authorization or historical repair and exposes no receipt DTO.
// budget is a trusted caller buffer bound, not a new product acceptance limit.
func CheckRunAdmissionPayload(raw []byte, budget int, id, workspace, pipelineID, runID string) error {
	if id == "" || workspace == "" || pipelineID == "" || runID == "" {
		return errRunAdmissionUnavailable
	}
	value, err := decodeRunAdmissionV1(raw, budget)
	if err != nil {
		return errRunAdmissionUnavailable
	}
	d := value.data
	if !bytes.Equal(d.ID, []byte(id)) || !bytes.Equal(d.RunID, []byte(runID)) || !bytes.Equal(d.Selection.WorkspaceID, []byte(workspace)) || !bytes.Equal(d.Selection.PipelineID, []byte(pipelineID)) {
		return errRunAdmissionUnavailable
	}
	return nil
}
