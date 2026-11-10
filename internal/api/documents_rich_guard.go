package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/crewship-ai/crewship/internal/documents"
)

const documentRichProofLimit = 500000

var documentRichID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// A schema-4 code block language is a short lowercase identifier, never markup
// (tools/docs-service/schema-core.mjs LANGUAGE).
var documentRichLanguage = regexp.MustCompile(`^[a-z0-9][a-z0-9+#-]{0,31}$`)

// The engine supplies this private proof, never a fresh client-owned scope.
// Every nontext node has a unique ID and a FULL subtree hash. Exact preorder
// authenticates root ordering too; the root itself has no attributes or marks.
type documentRichProof struct {
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

func documentRichSemanticGuard(semantic *documentSemanticPatch, hashes map[string]string, projection json.RawMessage, next documents.MutationGuard) documents.MutationGuard {
	return documentRichSemanticGuardFor(3, semantic, hashes, projection, next)
}

// documentRichSemanticGuardFor proves, for exactly one content schema, that
// the engine's candidate differs from the confirmed head only by the declared
// replacement of one paragraph or heading's text. Proposals and drafts use
// the Doc's own stored schema. Schema 5 dates and mentions have no width in
// text offsets; an edit that inserts beside one or deletes across one is
// refused. The engine's rule (ambiguous_inline_atom) is stricter: it also
// refuses every position a change could slide to within the agent's range,
// which Go does not see. This guard is the necessary condition: the result is
// exactly Go's own reconstruction of the declared edit, so no atom moves.
func documentRichSemanticGuardFor(schema int, semantic *documentSemanticPatch, hashes map[string]string, projection json.RawMessage, next documents.MutationGuard) documents.MutationGuard {
	return func(ctx context.Context, tx *sql.Tx, actor string, state documents.State) error {
		if state.Schema != schema {
			return documents.ErrSchema
		}
		if semantic == nil || semantic.Edit == nil || semantic.Rich == nil || len(hashes) != 1 {
			return documents.ErrConflict
		}
		before, err := documentRichTreeFor(semantic.Rich.Before, schema)
		if err != nil {
			return documents.ErrConflict
		}
		after, err := documentRichTreeFor(semantic.Rich.After, schema)
		if err != nil {
			return documents.ErrConflict
		}
		if !documentRichProjectionMatches(before, state.Projection) || !documentRichProjectionMatches(after, projection) {
			return documents.ErrConflict
		}
		target := documentRichFind(before, semantic.Target)
		if target == nil || (target["type"] != "paragraph" && target["type"] != "heading") || hashes[semantic.Target] != documentRichHash(target) {
			return documents.ErrConflict
		}
		children, _ := target["content"].([]any)
		var original strings.Builder
		for _, child := range children {
			if text, ok := child.(map[string]any)["text"].(string); ok {
				original.WriteString(text)
			}
		}
		units := utf16.Encode([]rune(original.String()))
		edit := semantic.Edit
		boundary := func(n int) bool {
			return n >= 0 && n <= len(units) && (n == 0 || n == len(units) || !(units[n-1] >= 0xd800 && units[n-1] <= 0xdbff && units[n] >= 0xdc00 && units[n] <= 0xdfff))
		}
		if !boundary(edit.From) || !boundary(edit.To) || edit.From > edit.To {
			return documents.ErrConflict
		}
		expected := string(utf16.Decode(units[:edit.From])) + edit.Insert + string(utf16.Decode(units[edit.To:]))
		if expected != semantic.Text {
			return documents.ErrConflict
		}
		// A replacement never starts or ends with the code point it replaces,
		// as the engine's minimal edit never does. Otherwise a proof could
		// "replace" marked text by the same unmarked text and drop its marks.
		// A pure deletion or insertion may sit at any equivalent position:
		// the agent picks among them with its range, deleting is within its
		// authority, and inserted text never carries marks.
		if edit.From < edit.To && edit.Insert != "" {
			deleted := []rune(string(utf16.Decode(units[edit.From:edit.To])))
			inserted := []rune(edit.Insert)
			if deleted[0] == inserted[0] || deleted[len(deleted)-1] == inserted[len(inserted)-1] {
				return documents.ErrConflict
			}
		}
		// An atom sits at a text offset. It stays before the edit when at or
		// before From, after it when at or after To; inside a deleted range,
		// or exactly beside inserted text, its side would be a guess.
		offset := 0
		for _, value := range children {
			child := value.(map[string]any)
			text, isText := child["text"].(string)
			if isText {
				offset += len(utf16.Encode([]rune(text)))
				continue
			}
			if (edit.From < offset && offset < edit.To) || (edit.Insert != "" && (offset == edit.From || offset == edit.To)) {
				return documents.ErrConflict
			}
		}
		// One pass in document order: the prefix before From, then the
		// inserted text, then the suffix from To. An atom at or before From
		// stays in the prefix, any later atom in the suffix; the checks above
		// leave no atom whose side depends on that choice.
		var replacement []any
		inserted := false
		insert := func() {
			if !inserted && edit.Insert != "" {
				replacement = append(replacement, map[string]any{"type": "text", "text": edit.Insert})
			}
			inserted = true
		}
		offset = 0
		for _, value := range children {
			child := value.(map[string]any)
			textValue, isText := child["text"].(string)
			if !isText {
				if offset > edit.From {
					insert()
				}
				replacement = append(replacement, child)
				continue
			}
			text := utf16.Encode([]rune(textValue))
			end := offset + len(text)
			part := func(from, to int) {
				if from < to {
					copied := map[string]any{}
					for k, v := range child {
						copied[k] = v
					}
					copied["text"] = string(utf16.Decode(text[from-offset : to-offset]))
					replacement = append(replacement, copied)
				}
			}
			part(offset, min(end, edit.From))
			if end > edit.From {
				insert()
				part(max(offset, edit.To), end)
			}
			offset = end
		}
		insert()
		if len(replacement) == 0 {
			delete(target, "content")
		} else {
			target["content"] = replacement
		}
		documentRichNormalize(before)
		documentRichNormalize(after)
		if !reflect.DeepEqual(before, after) {
			return documents.ErrConflict
		}
		if next != nil {
			return next(ctx, tx, actor, state)
		}
		return nil
	}
}

func documentRichTree(raw json.RawMessage) (map[string]any, error) {
	return documentRichTreeFor(raw, 3)
}

// documentRichPalette is the closed schema-5 colour palette
// (tools/docs-service/schema-core.mjs PALETTE): names, never CSS values.
var documentRichPalette = map[string]bool{"gray": true, "red": true, "orange": true, "yellow": true, "green": true, "blue": true, "purple": true, "pink": true}

// A schema-5 mention stores only an opaque user id.
var documentRichUserID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// documentRichDate is one real calendar day, YYYY-MM-DD, year 1 or later
// (schema-core.mjs validDate).
func documentRichDate(value any) bool {
	text, ok := value.(string)
	if !ok || len(text) != 10 || text[4] != '-' || text[7] != '-' {
		return false
	}
	digits := func(part string) (int, bool) {
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, false
			}
			n = n*10 + int(r-'0')
		}
		return n, true
	}
	year, okYear := digits(text[:4])
	month, okMonth := digits(text[5:7])
	day, okDay := digits(text[8:])
	if !okYear || !okMonth || !okDay || year < 1 || month < 1 || month > 12 || day < 1 {
		return false
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[month-1]
	if month == 2 && year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		days = 29
	}
	return day <= days
}

// documentRichTreeFor validates a closed rich projection for schema 3 to 6.
// Schema 4 adds bold, italic and strike (combinable with each other and a
// link; inline code stays alone), code block languages, quotes and task lists.
// Schema 5 adds underline, a palette text colour and highlight, and the inline
// atoms date {value} and mention {userId} inside paragraphs and headings.
// Schema 6 (Canvas) has exactly schema 5's Page tree; its surface is never
// part of a projection.
func documentRichTreeFor(raw json.RawMessage, schema int) (map[string]any, error) {
	if !documents.RichSchema(schema) {
		return nil, fmt.Errorf("unsupported rich schema")
	}
	formatted := schema >= 4
	latest := schema >= 5
	if len(raw) == 0 || len(raw) > documentRichProofLimit {
		return nil, fmt.Errorf("bounded rich proof required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid proof")
	}
	ids := map[string]bool{}
	count := 0
	var visit func(map[string]any, string, int) error
	visit = func(node map[string]any, parent string, depth int) error {
		count++
		if depth > 40 || count > 10000 {
			return fmt.Errorf("bounded tree required")
		}
		typ, ok := node["type"].(string)
		if !ok {
			return fmt.Errorf("node type required")
		}
		for key := range node {
			if key != "type" && key != "attrs" && key != "content" && key != "text" && key != "marks" {
				return fmt.Errorf("unknown node field")
			}
		}
		if latest && (typ == "date" || typ == "mention") {
			// An atom is a leaf with exactly one attribute: no text, content or marks.
			if parent != "paragraph" && parent != "heading" || len(node) != 2 {
				return fmt.Errorf("inline atom shape")
			}
			attrs, ok := node["attrs"].(map[string]any)
			if !ok || len(attrs) != 1 {
				return fmt.Errorf("inline atom attrs")
			}
			if typ == "date" && !documentRichDate(attrs["value"]) {
				return fmt.Errorf("inline date")
			}
			if typ == "mention" {
				if id, ok := attrs["userId"].(string); !ok || !documentRichUserID.MatchString(id) {
					return fmt.Errorf("inline mention")
				}
			}
			return nil
		}
		if typ == "text" {
			if parent != "paragraph" && parent != "heading" && parent != "codeBlock" {
				return fmt.Errorf("text parent required")
			}
			text, ok := node["text"].(string)
			if !ok || text == "" || node["attrs"] != nil || node["content"] != nil {
				return fmt.Errorf("text shape")
			}
			if marks, exists := node["marks"]; exists {
				list, ok := marks.([]any)
				limit := 1
				if latest {
					limit = 7
				} else if formatted {
					limit = 4
				}
				if !ok || len(list) > limit {
					return fmt.Errorf("marks shape")
				}
				kinds := map[any]bool{}
				for _, value := range list {
					mark, ok := value.(map[string]any)
					if !ok {
						return fmt.Errorf("mark shape")
					}
					kind := mark["type"]
					emphasis := formatted && (kind == "bold" || kind == "italic" || kind == "strike") || latest && kind == "underline"
					colour := latest && (kind == "textColor" || kind == "highlight")
					if kind != "link" && kind != "code" && !emphasis && !colour {
						return fmt.Errorf("mark type")
					}
					if kinds[kind] || (kind == "code" && len(list) > 1) {
						return fmt.Errorf("mark combination")
					}
					kinds[kind] = true
					for key := range mark {
						if key != "type" && key != "attrs" {
							return fmt.Errorf("mark field")
						}
					}
					if kind == "code" || emphasis {
						if attrs, exists := mark["attrs"]; exists {
							m, ok := attrs.(map[string]any)
							if !ok || len(m) != 0 {
								return fmt.Errorf("code attrs")
							}
						}
					} else if colour {
						attrs, ok := mark["attrs"].(map[string]any)
						if !ok || len(attrs) != 1 {
							return fmt.Errorf("colour attrs")
						}
						if name, ok := attrs["color"].(string); !ok || !documentRichPalette[name] {
							return fmt.Errorf("palette colour")
						}
					} else {
						attrs, ok := mark["attrs"].(map[string]any)
						if !ok || len(attrs) != 1 {
							return fmt.Errorf("link attrs")
						}
						if href, ok := attrs["href"].(string); !ok || len(href) > 2048 {
							return fmt.Errorf("link href")
						}
					}
				}
				if parent == "codeBlock" && len(list) > 0 {
					return fmt.Errorf("code block marks")
				}
			}
			return nil
		}
		if node["text"] != nil {
			return fmt.Errorf("block text")
		}
		if _, exists := node["marks"]; exists {
			return fmt.Errorf("block marks")
		}
		if depth == 0 {
			if typ != "doc" || len(node) != 2 || node["attrs"] != nil {
				return fmt.Errorf("closed root required")
			}
		} else {
			if parent == "paragraph" || parent == "heading" || parent == "codeBlock" {
				return fmt.Errorf("inline text required")
			}
			allowed := map[string]bool{"id": true}
			switch typ {
			case "paragraph", "table", "tableRow", "bulletList", "listItem":
			case "codeBlock":
				if formatted {
					allowed["language"] = true
				}
			case "blockquote", "taskList":
				if !formatted {
					return fmt.Errorf("block type")
				}
			case "taskItem":
				if !formatted {
					return fmt.Errorf("block type")
				}
				allowed["checked"] = true
			case "heading":
				allowed["level"] = true
			case "orderedList":
				allowed["start"] = true
				allowed["type"] = true
			case "tableCell", "tableHeader":
				for _, k := range []string{"colspan", "rowspan", "colwidth", "align"} {
					allowed[k] = true
				}
			default:
				return fmt.Errorf("block type")
			}
			attrs, ok := node["attrs"].(map[string]any)
			if !ok {
				return fmt.Errorf("block attrs")
			}
			id, ok := attrs["id"].(string)
			if !ok || !documentRichID.MatchString(id) || ids[id] {
				return fmt.Errorf("unique block ID required")
			}
			ids[id] = true
			for key := range attrs {
				if !allowed[key] {
					return fmt.Errorf("unknown block attrs")
				}
			}
			for key, value := range attrs {
				if key == "id" {
					continue
				}
				switch key {
				case "level":
					if value != json.Number("1") && value != json.Number("2") && value != json.Number("3") {
						return fmt.Errorf("heading level")
					}
				case "start":
					number, ok := value.(json.Number)
					if !ok {
						return fmt.Errorf("list start")
					}
					n, err := number.Int64()
					if err != nil || n < 1 || n > 10000 {
						return fmt.Errorf("list start")
					}
				case "colspan", "rowspan":
					if value != json.Number("1") {
						return fmt.Errorf("simple table span")
					}
				case "language":
					if language, ok := value.(string); !ok || !documentRichLanguage.MatchString(language) {
						return fmt.Errorf("code block language")
					}
				case "checked":
					if _, ok := value.(bool); !ok {
						return fmt.Errorf("task item state")
					}
				case "type", "colwidth", "align":
					if value != nil {
						return fmt.Errorf("unsupported block attribute")
					}
				}
			}
		}
		if content, exists := node["content"]; exists {
			list, ok := content.([]any)
			if !ok {
				return fmt.Errorf("content array")
			}
			for _, value := range list {
				child, ok := value.(map[string]any)
				if !ok {
					return fmt.Errorf("child shape")
				}
				if err := visit(child, typ, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(root, "", 0); err != nil {
		return nil, err
	}
	return root, nil
}
func documentRichFind(node map[string]any, id string) map[string]any {
	if attrs, ok := node["attrs"].(map[string]any); ok && attrs["id"] == id {
		return node
	}
	children, _ := node["content"].([]any)
	for _, v := range children {
		if found := documentRichFind(v.(map[string]any), id); found != nil {
			return found
		}
	}
	return nil
}
func documentRichProjectionMatches(root map[string]any, raw json.RawMessage) bool {
	var blocks []DocumentBlockHash
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	index := 0
	var visit func(map[string]any) bool
	visit = func(node map[string]any) bool {
		// Blocks carry an ID; schema-5 inline atoms have attrs but no ID and
		// are hashed only as part of their block.
		if attrs, ok := node["attrs"].(map[string]any); ok && attrs["id"] != nil {
			if index >= len(blocks) || blocks[index].ID != attrs["id"] || blocks[index].Hash != documentRichHash(node) {
				return false
			}
			index++
		}
		children, _ := node["content"].([]any)
		for _, v := range children {
			if !visit(v.(map[string]any)) {
				return false
			}
		}
		return true
	}
	return visit(root) && index == len(blocks)
}
func documentRichNormalize(node map[string]any) {
	children, ok := node["content"].([]any)
	if !ok {
		return
	}
	var normalized []any
	for _, value := range children {
		child := value.(map[string]any)
		documentRichNormalize(child)
		if child["type"] == "text" {
			if marks, ok := child["marks"].([]any); ok && len(marks) == 0 {
				delete(child, "marks")
			}
			if len(normalized) > 0 {
				last := normalized[len(normalized)-1].(map[string]any)
				if last["type"] == "text" && reflect.DeepEqual(last["marks"], child["marks"]) {
					last["text"] = last["text"].(string) + child["text"].(string)
					continue
				}
			}
		}
		normalized = append(normalized, child)
	}
	if len(normalized) == 0 {
		delete(node, "content")
	} else {
		node["content"] = normalized
	}
}

// Mirrors JSON.stringify's scalar encoding, including literal U+2028/U+2029
// and HTML characters; Go's encoding/json deliberately escapes those scalars.
func documentRichCanonical(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		var out strings.Builder
		out.WriteByte('"')
		for _, r := range v {
			switch r {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case '\b':
				out.WriteString(`\b`)
			case '\f':
				out.WriteString(`\f`)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if r < 32 {
					fmt.Fprintf(&out, `\u%04x`, r)
				} else {
					out.WriteRune(r)
				}
			}
		}
		out.WriteByte('"')
		return out.String()
	case json.Number:
		return string(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			parts[i] = documentRichCanonical(x)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, documentRichCanonical(key)+":"+documentRichCanonical(v[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		panic("unvalidated rich canonical value")
	}
}
func documentRichHash(node map[string]any) string {
	sum := sha256.Sum256([]byte("docs-d0-c14n-v1\n" + documentRichCanonical(node)))
	return hex.EncodeToString(sum[:])
}
