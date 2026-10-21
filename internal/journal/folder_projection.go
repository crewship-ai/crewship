package journal

import "maps"

// PublicFolderMembership projects only the folder-move event. The durable
// target and original summary remain canonical; neither a feed nor an
// automation rule is authority to disclose a private Page. Old or malformed
// classifications fail closed, including historical ordinary Page events.
func PublicFolderMembership(e Entry) Entry {
	if e.Type != "page.folder_membership_changed" {
		return e
	}
	audience, audienceOK := e.Payload["private_audience"].(bool)
	metadata, metadataOK := e.Payload["private_output_metadata"].(bool)
	payload := maps.Clone(e.Payload)
	delete(payload, "private_audience")
	delete(payload, "private_output_metadata")
	if audienceOK && metadataOK && !audience && !metadata {
		e.Payload = payload
		return e
	}
	e.Summary = "Page folder membership changed"
	e.Refs = nil
	safe := make(map[string]any)
	for _, key := range []string{"op", "from", "to", "pages_version", "actor_user_id"} {
		if value, ok := payload[key]; ok {
			safe[key] = value
		}
	}
	// A known non-private output may invalidate by opaque ID, never by slug.
	if audienceOK && metadataOK && !audience {
		if id, ok := payload["page_id"].(string); ok {
			safe["page_id"] = id
		}
	}
	e.Payload = safe
	return e
}

// Shared List/Count FTS predicate. JSON access is guarded by CASE so malformed
// historical payloads are unavailable to title search, rather than SQL errors.
const publicFolderSearchSQL = `(CASE WHEN journal_entries.entry_type <> 'page.folder_membership_changed' THEN 1
 WHEN json_valid(journal_entries.payload) THEN CASE
 WHEN json_type(journal_entries.payload,'$.private_audience')='false'
 AND json_type(journal_entries.payload,'$.private_output_metadata')='false' THEN 1 ELSE 0 END
 ELSE 0 END)`
