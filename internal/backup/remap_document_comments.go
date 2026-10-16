package backup

import "encoding/json"

// Comment IDs are forked with their rows. The anchor repeats that identity;
// relative CRDT positions and stable document block IDs remain byte-for-byte
// values from the retained document history.
func remapDocumentCommentAnchors(dump *DBDump, idMap map[string]map[string]string) {
	for _, row := range dump.Tables["document_comments"] {
		raw, ok := row["anchor_json"].(string)
		if !ok {
			continue
		}
		var anchor map[string]any
		if json.Unmarshal([]byte(raw), &anchor) != nil {
			continue
		}
		old, _ := anchor["id"].(string)
		if mapped := idMap["document_comments"][old]; mapped != "" {
			anchor["id"] = mapped
			if encoded, err := json.Marshal(anchor); err == nil {
				row["anchor_json"] = string(encoded)
			}
		}
	}
}
