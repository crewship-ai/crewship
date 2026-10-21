package api

import "github.com/crewship-ai/crewship/internal/harbormaster"

// approvalReadProjection keeps host-only whole-run provenance out of the legacy
// workspace approval read surfaces. Authorized chat source adapters read the
// stored request internally. Copy the map so serialization cannot alter that source.
// Existing payload fields, actual gate mode and legacy approval rights remain.
func approvalReadProjection(row harbormaster.Request) harbormaster.Request {
	if row.Payload["tool"] != "agent_run" {
		return row
	}
	if _, ok := row.Payload["correlation"]; !ok {
		return row
	}
	payload := make(map[string]any, len(row.Payload)-1)
	for key, value := range row.Payload {
		if key != "correlation" {
			payload[key] = value
		}
	}
	row.Payload = payload
	return row
}
