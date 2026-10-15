package backup

// Preserve historical instructions and results without restoring admission
// receipts or resuming paid work. Never modify the caller's backup payload.
func safeArtifactRequestRestoreRow(table string, row map[string]any) map[string]any {
	if table != "page_artifact_requests" && table != "page_board_requests" && table != "document_requests" {
		return row
	}
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = v
	}
	if table != "document_requests" {
		out["notice_pending"] = 0
	}
	out["receipt_json"] = "{}"
	if table == "page_artifact_requests" || table == "document_requests" {
		out["run_id"] = nil
	} else {
		out["receipt_json"] = ""
	}
	if row["status"] == "queued" || row["status"] == "running" {
		out["status"] = "failed"
		out["error"] = "Agent work suspended by backup restore; send a new request to run again"
	}
	return out
}
