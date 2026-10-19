package api

import (
	"encoding/json"
	"errors"
	"unicode/utf16"
	"unicode/utf8"
)

// Original text-selection preparation only; nothing calls this helper from a
// route, tool or run admission. It confers neither consent nor read/write access.
// Derived from #3268 at 22b081c8e34bc5b7509ad2b708d9386fee102c40:
// documents_contract.go DocumentSelectedTarget and documentVerifiedTarget;
// documents_request_bounds.go validDocumentTarget; documents_agent_requests.go
// CreateDocumentRequest's schema3 and 4096 UTF-16 selection checks.
// The structural branch, runtime admission and legacy schemas are not extracted.
type documentEditTextSelection struct {
	ID    string `json:"id"`
	Hash  string `json:"hash"`
	From  int    `json:"from"`
	To    int    `json:"to"`
	Quote string `json:"quote"`
}

// This is original host-captured state, NOT model-supplied or freshly read
// replacement authority. The caller must retain its original Page, revision,
// schema, selection and hash under the eventual trusted request contract. These
// inner types intentionally have no Page/revision/run/user/mode fields.
type documentEditVerifiedTextSelection struct {
	documentEditTextSelection
	Text   string
	Schema int
}

func decodeDocumentEditTextSelection(raw []byte) (documentEditTextSelection, error) {
	var selected documentEditTextSelection
	if len(raw) > 64<<10 {
		return selected, errors.New("bounded text selection required")
	}
	// Reuse the installed object guard: duplicate keys (including escaped keys),
	// null fields, invalid UTF-8 and a second JSON value are rejected.
	fields, err := documentDraftObject(raw)
	if err != nil {
		return selected, err
	}
	if len(fields) != 5 {
		return selected, errors.New("closed text selection required")
	}
	for _, name := range []string{"id", "hash", "from", "to", "quote"} {
		if fields[name] == nil {
			return selected, errors.New("missing text selection field")
		}
	}
	if json.Unmarshal(raw, &selected) != nil || !documentEditSelectionShape(selected) {
		return documentEditTextSelection{}, errors.New("invalid text selection")
	}
	return selected, nil
}

func documentEditSelectionShape(selected documentEditTextSelection) bool {
	return documentRichID.MatchString(selected.ID) && documentDraftHash(selected.Hash) &&
		selected.From >= 0 && selected.From < selected.To && utf8.ValidString(selected.Quote) &&
		len(selected.Quote) > 0 && len(utf16.Encode([]rune(selected.Quote))) <= 4096
}

func validDocumentEditTextSelection(original documentEditVerifiedTextSelection) bool {
	if original.Schema != 3 || !documentEditSelectionShape(original.documentEditTextSelection) || !utf8.ValidString(original.Text) {
		return false
	}
	units := utf16.Encode([]rune(original.Text))
	boundary := func(n int) bool {
		return n >= 0 && n <= len(units) && (n == 0 || n == len(units) ||
			!(units[n-1] >= 0xD800 && units[n-1] <= 0xDBFF && units[n] >= 0xDC00 && units[n] <= 0xDFFF))
	}
	return boundary(original.From) && boundary(original.To) &&
		string(utf16.Decode(units[original.From:original.To])) == original.Quote
}

// Equality preserves the ORIGINAL captured hash/range; it does not compute a
// hash from this text or verify a current stored revision. The caller must do
// both full-tree dependency checks and live authorization inside the writer
// transaction. A later read can supply context but cannot replace original.
func matchesOriginalDocumentEditTextSelection(selected documentEditTextSelection, original documentEditVerifiedTextSelection) bool {
	return validDocumentEditTextSelection(original) && selected == original.documentEditTextSelection
}
