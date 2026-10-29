package api

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Selection belongs only to the exact committed operation receipt. Never add
// it to peer checkpoint projection, infer it from input, or treat it as Saved.
func commandReceiptSelection(receipt, checkpoint []byte, command string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(receipt, &fields) != nil {
		return nil, errCommandStore
	}
	raw, found := fields["selection"]
	// Participant undo is host preimage reversal, not a native command returning
	// a selection. Do not fabricate a caret for this currently unqualified case.
	if command == "participant.undo" {
		if found {
			return nil, errCommandStore
		}
		return nil, nil
	}
	if !found || len(raw) == 0 || len(raw) > 2048 {
		return nil, errCommandStore
	}
	canonical, err := mooring.CanonicalJSON(raw)
	if err != nil {
		return nil, errCommandStore
	}
	var selected map[string]json.RawMessage
	if json.Unmarshal(canonical, &selected) != nil || len(selected) != 2 || selected["anchor"] == nil || selected["focus"] == nil {
		return nil, errCommandStore
	}
	var cp struct {
		P struct {
			IDs    []string `json:"ids"`
			Native struct {
				Body []struct {
					Text *string `json:"text"`
				} `json:"body"`
			} `json:"native"`
		} `json:"p"`
	}
	if json.Unmarshal(checkpoint, &cp) != nil || len(cp.P.IDs) == 0 || len(cp.P.IDs) != len(cp.P.Native.Body) || len(cp.P.IDs) > 4096 {
		return nil, errCommandStore
	}
	for _, key := range []string{"anchor", "focus"} {
		var pos map[string]json.RawMessage
		if json.Unmarshal(selected[key], &pos) != nil || len(pos) != 3 {
			return nil, errCommandStore
		}
		var story string
		var path []uint64
		var off uint64
		if json.Unmarshal(pos["story"], &story) != nil || story != "body" || json.Unmarshal(pos["path"], &path) != nil || len(path) != 1 || path[0] >= uint64(len(cp.P.IDs)) || string(pos["off"]) == "null" || json.Unmarshal(pos["off"], &off) != nil {
			return nil, errCommandStore
		}
		textValue := cp.P.Native.Body[path[0]].Text
		if textValue == nil {
			return nil, errCommandStore
		}
		text := *textValue
		if !utf8.ValidString(text) || off > uint64(len(text)) || (off < uint64(len(text)) && !utf8.RuneStart(text[off])) {
			return nil, errCommandStore
		}
	}
	return append(json.RawMessage(nil), raw...), nil
}
