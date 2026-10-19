package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func originalEditSelection() documentEditVerifiedTextSelection {
	return documentEditVerifiedTextSelection{
		documentEditTextSelection: documentEditTextSelection{ID: "intro", Hash: strings.Repeat("a", 64), From: 1, To: 3, Quote: "😀"},
		Text:                      "a😀z", Schema: 3,
	}
}

func TestDocumentEditSelectionClosedDecode(t *testing.T) {
	original := originalEditSelection()
	raw, err := json.Marshal(original.documentEditTextSelection)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeDocumentEditTextSelection(raw)
	if err != nil || got != original.documentEditTextSelection {
		t.Fatalf("decode=%+v err=%v", got, err)
	}
	cases := []string{
		`null`, `[]`, `{}`, string(raw) + ` {}`, string(raw) + ` trailing`,
		strings.TrimSuffix(string(raw), "}") + `,"extra":true}`,
		strings.TrimSuffix(string(raw), "}") + `,"id":"other"}`,
		strings.Replace(string(raw), `"from":1`, `"from":null`, 1),
		strings.Replace(string(raw), `"from":1`, `"from":1.5`, 1),
		strings.Replace(string(raw), `"id":"intro",`, ``, 1),
		strings.Replace(string(raw), `"hash":"`+original.Hash+`"`, `"hash":"fresh"`, 1),
		strings.Replace(string(raw), `"id":"intro"`, `"id":"../other"`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"structure":{"block_ids":["intro"]}}`,
		strings.TrimSuffix(string(raw), "}") + `,"from":0}`,
		string([]byte{'{', '"', 0xff, '"', ':', '1', '}'}),
		strings.Repeat(" ", 64<<10) + string(raw),
	}
	for i, value := range cases {
		if _, err := decodeDocumentEditTextSelection([]byte(value)); err == nil {
			t.Errorf("case%d accepted %q", i, value[:min(len(value), 180)])
		}
	}
}

func TestDocumentEditSelectionOriginalUTF16Range(t *testing.T) {
	original := originalEditSelection()
	if !validDocumentEditTextSelection(original) {
		t.Fatal("original emoji selection rejected")
	}
	for _, mutate := range []struct {
		name   string
		change func(*documentEditVerifiedTextSelection)
	}{
		{"split start", func(v *documentEditVerifiedTextSelection) { v.From = 2 }},
		{"split end", func(v *documentEditVerifiedTextSelection) { v.To = 2 }},
		{"negative", func(v *documentEditVerifiedTextSelection) { v.From = -1 }},
		{"outside", func(v *documentEditVerifiedTextSelection) { v.To = 5 }},
		{"empty", func(v *documentEditVerifiedTextSelection) { v.To = v.From; v.Quote = "" }},
		{"reversed", func(v *documentEditVerifiedTextSelection) { v.From = 3; v.To = 1 }},
		{"wrong quote", func(v *documentEditVerifiedTextSelection) { v.Quote = "a" }},
		{"future schema", func(v *documentEditVerifiedTextSelection) { v.Schema = 4 }},
		{"legacy schema", func(v *documentEditVerifiedTextSelection) { v.Schema = 2 }},
		{"invalid ID", func(v *documentEditVerifiedTextSelection) { v.ID = "" }},
		{"invalid hash", func(v *documentEditVerifiedTextSelection) { v.Hash = strings.Repeat("A", 64) }},
		{"oversize quote", func(v *documentEditVerifiedTextSelection) {
			v.Text = strings.Repeat("a", 4097)
			v.Quote = v.Text
			v.From = 0
			v.To = 4097
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			value := original
			mutate.change(&value)
			if validDocumentEditTextSelection(value) {
				t.Fatal("invalid original accepted")
			}
		})
	}
	full := original
	full.From = 0
	full.To = 4
	full.Quote = full.Text
	if !validDocumentEditTextSelection(full) {
		t.Fatal("whole original paragraph rejected")
	}
}

func TestDocumentEditSelectionDoesNotRefreshOriginalMandate(t *testing.T) {
	original := originalEditSelection()
	if !matchesOriginalDocumentEditTextSelection(original.documentEditTextSelection, original) {
		t.Fatal("original rejected")
	}
	for _, mutate := range []func(*documentEditTextSelection){
		func(v *documentEditTextSelection) { v.ID = "neighbor" },
		func(v *documentEditTextSelection) { v.Hash = strings.Repeat("b", 64) },
		func(v *documentEditTextSelection) { v.From = 0; v.To = 4; v.Quote = original.Text },
		func(v *documentEditTextSelection) { v.Quote = "fresh" },
	} {
		value := original.documentEditTextSelection
		mutate(&value)
		if matchesOriginalDocumentEditTextSelection(value, original) {
			t.Fatal("changed selection accepted")
		}
	}
	invalid := original
	invalid.Schema = 4
	if matchesOriginalDocumentEditTextSelection(invalid.documentEditTextSelection, invalid) {
		t.Fatal("invalid original accepted")
	}
}
