package journal

import "time"

// SerializeEntry turns one Entry into the JSON-friendly map that both the
// SSE stream (internal/api journal handler) and the WebSocket journal
// bridge (internal/server) put on the wire. Keeping ONE serializer means
// a journal.entry frame is byte-for-byte the same shape whether it arrived
// over SSE or WS, so the frontend parses both with the single
// journalEntrySchema (lib/types/journal.ts).
//
// TS is rendered as RFC3339Nano UTC so the browser Date constructor parses
// it directly. Empty optional ids/maps are omitted rather than sent as ""
// / {} to keep frames small and match the zod schema's optional fields.
func SerializeEntry(e Entry) map[string]any {
	row := map[string]any{
		"id":           e.ID,
		"workspace_id": e.WorkspaceID,
		"ts":           e.TS.UTC().Format(time.RFC3339Nano),
		"entry_type":   string(e.Type),
		"severity":     string(e.Severity),
		"priority":     string(e.Priority),
		"actor_type":   string(e.ActorType),
		"summary":      e.Summary,
	}
	if e.CrewID != "" {
		row["crew_id"] = e.CrewID
	}
	if e.AgentID != "" {
		row["agent_id"] = e.AgentID
	}
	if e.MissionID != "" {
		row["mission_id"] = e.MissionID
	}
	if e.ActorID != "" {
		row["actor_id"] = e.ActorID
	}
	if e.TraceID != "" {
		row["trace_id"] = e.TraceID
	}
	if len(e.Payload) > 0 {
		row["payload"] = e.Payload
	}
	if len(e.Refs) > 0 {
		row["refs"] = e.Refs
	}
	return row
}

// SerializeEntries maps SerializeEntry over a slice.
func SerializeEntries(entries []Entry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, SerializeEntry(e))
	}
	return out
}
