package backup

import (
	"encoding/json"
	"testing"
)

func TestBoardRequestForkTypedReferences(t *testing.T) {
	d := &DBDump{Tables: map[string][]map[string]any{"page_board_requests": {{"board_page_id": "board", "result_page_id": "source", "snapshot_json": `{"elements":[{"id":"drawing","type":"image","fileId":"file","text":"file"}],"placements":[{"id":"pin","x":1}]}`, "selected_placement_ids_json": `["pin"]`}}, "page_board_request_sources": {{"source_page_id": "source"}}}}
	ids := map[string]map[string]string{"pages": {"board": "fork-board", "source": "fork-source"}, "page_board_files": {"file": "fork-file"}, "page_board_artifact_placements": {"pin": "fork-pin"}}
	remapBoardPageReferences(d, ids)
	row := d.Tables["page_board_requests"][0]
	if row["board_page_id"] != "fork-board" || row["result_page_id"] != "fork-source" || d.Tables["page_board_request_sources"][0]["source_page_id"] != "fork-source" {
		t.Fatal(d)
	}
	var snapshot struct {
		Elements   []map[string]any `json:"elements"`
		Placements []map[string]any `json:"placements"`
	}
	if err := json.Unmarshal([]byte(row["snapshot_json"].(string)), &snapshot); err != nil {
		t.Fatal(err)
	}
	el := snapshot.Elements[0]
	if el["id"] != "drawing" || el["fileId"] != "fork-file" || el["text"] != "file" || snapshot.Placements[0]["id"] != "fork-pin" || row["selected_placement_ids_json"] != `["fork-pin"]` {
		t.Fatal(row)
	}
}
