package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExportEnvelopeStrictSnapshotAndPrivateAssetBinding(t *testing.T) {
	original := corpus()
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeExportInput(raw)
	if err != nil || decoded.ContentHash != original.ContentHash || len(decoded.Deck.Slides) != 5 || !strings.Contains(decoded.Deck.Slides[0].Elements[0].Text.Value, "\n") {
		t.Fatal("valid saved corpus refused", err)
	}
	for _, test := range []struct {
		name   string
		change func(exportInput) exportInput
	}{
		{"original_hash", func(v exportInput) exportInput { v.ContentHash = strings.Repeat("f", 64); return v }},
		{"missing_private_image", func(v exportInput) exportInput { v.Assets = []exportAsset{}; return v }},
		{"wrong_asset_page_identity", func(v exportInput) exportInput { v.Assets[0].AssetID = "other_page_asset"; return v }},
		{"changed_asset_bytes", func(v exportInput) exportInput { v.Assets[0].Data = "aGVsbG8="; return v }},
		{"wrong_actual_dimensions", func(v exportInput) exportInput { v.Assets[0].Width++; return v }},
		{"wrong_actual_mime", func(v exportInput) exportInput { v.Assets[0].MIME = "image/jpeg"; return v }},
		{"duplicate_asset_identity", func(v exportInput) exportInput { v.Assets = append(v.Assets, v.Assets[0]); return v }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, e := json.Marshal(test.change(corpus()))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = decodeExportInput(data); e == nil {
				t.Fatal("untrusted export envelope admitted")
			}
		})
	}
	for _, test := range []struct{ name, raw string }{
		{"case_fold_overwrite", strings.Replace(string(raw), `"revision":7`, `"revision":7,"REVISION":99`, 1)},
		{"duplicate_field", strings.Replace(string(raw), `"revision":7`, `"revision":7,"revision":99`, 1)},
		{"trailing_json", string(raw) + ` {}`},
		{"null_asset_array", strings.Replace(string(raw), `"assets":[`, `"assets":null,"wrong_assets":[`, 1)},
		{"unknown_asset_key", strings.Replace(string(raw), `"asset_id":`, `"ASSET_ID":"other","asset_id":`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, e := decodeExportInput([]byte(test.raw)); e == nil {
				t.Fatal("strict JSON bypass accepted")
			}
		})
	}
}

func TestExportJSONStructuralBudgetsBeforeModelDecode(t *testing.T) {
	for _, raw := range []string{strings.Repeat("[", 10000) + "0" + strings.Repeat("]", 10000), "[" + strings.Repeat("0,", maxJSONValues) + "0]"} {
		if err := rejectDuplicateKeys([]byte(raw)); err == nil || !strings.Contains(err.Error(), "budget") {
			t.Fatal("unbounded JSON structure admitted", err)
		}
	}
	if err := rejectDuplicateKeys([]byte(`{"safe":[{"text":"Český text"}]}`)); err != nil {
		t.Fatal("bounded JSON refused", err)
	}
}
