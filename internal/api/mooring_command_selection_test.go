package api

import (
	"strings"
	"testing"
)

func TestCommandReceiptSelectionBoundedNativeUTF8(t *testing.T) {
	checkpoint := []byte(`{"p":{"ids":["doc:g1:1","doc:g1:2"],"native":{"body":[{"text":"čA"},{"text":""}]}}}`)
	valid := `{"anchor":{"story":"body","path":[0],"off":2},"focus":{"story":"body","path":[1],"off":0}}`
	receipt := func(selection string) []byte { return []byte(`{"selection":` + selection + `}`) }
	if _, err := commandReceiptSelection(receipt(valid), checkpoint, "text.insert"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"missing": "", "null": "null", "interior_utf8": strings.Replace(valid, `"off":2`, `"off":1`, 1), "foreign_block": strings.Replace(valid, `"path":[0]`, `"path":[2]`, 1), "null_offset": strings.Replace(valid, `"off":2`, `"off":null`, 1), "negative_offset": strings.Replace(valid, `"off":2`, `"off":-1`, 1), "unknown_field": strings.Replace(valid, `"off":2`, `"off":2,"extra":true`, 1), "duplicate": strings.Replace(valid, `"off":2`, `"off":2,"off":2`, 1), "oversized_multibyte": strings.TrimSuffix(valid, "}") + `,"padding":"` + strings.Repeat("é", 1024) + `"}`}
	for name, selection := range cases {
		t.Run(name, func(t *testing.T) {
			raw := receipt(selection)
			if name == "missing" {
				raw = []byte(`{}`)
			}
			if _, err := commandReceiptSelection(raw, checkpoint, "text.insert"); err == nil {
				t.Fatal("bad selection accepted")
			}
		})
	}
	if _, e := commandReceiptSelection(receipt(valid), []byte(`{"p":{"ids":["a","b"],"native":{"body":[{},{}]}}}`), "text.insert"); e == nil {
		t.Fatal("missing native text accepted")
	}

	if raw, e := commandReceiptSelection([]byte(`{}`), checkpoint, "participant.undo"); e != nil || raw != nil {
		t.Fatal("undo selection fabricated", e)
	}
	if _, e := commandReceiptSelection(receipt(valid), checkpoint, "participant.undo"); e == nil {
		t.Fatal("undo selection accepted without native result")
	}
}
