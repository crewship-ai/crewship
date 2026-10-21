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

// The engine supplies this private proof, never a fresh client-owned scope.
// Every nontext node has a unique ID and a FULL subtree hash. Exact preorder
// authenticates root ordering too; the root itself has no attributes or marks.
type documentRichProof struct {
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

func documentRichSemanticGuard(semantic *documentSemanticPatch, hashes map[string]string, projection json.RawMessage, next documents.MutationGuard) documents.MutationGuard {
	return func(ctx context.Context, tx *sql.Tx, actor string, state documents.State) error {
		if state.Schema != 3 {
			return documents.ErrSchema
		}
		if semantic == nil || semantic.Edit == nil || semantic.Rich == nil || len(hashes) != 1 {
			return documents.ErrConflict
		}
		before, err := documentRichTree(semantic.Rich.Before)
		if err != nil {
			return documents.ErrConflict
		}
		after, err := documentRichTree(semantic.Rich.After)
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
			original.WriteString(child.(map[string]any)["text"].(string))
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
		var replacement []any
		slice := func(from, to int) {
			offset := 0
			for _, value := range children {
				child := value.(map[string]any)
				text := utf16.Encode([]rune(child["text"].(string)))
				start, end := max(from-offset, 0), min(to-offset, len(text))
				if start < end {
					part := map[string]any{}
					for k, v := range child {
						part[k] = v
					}
					part["text"] = string(utf16.Decode(text[start:end]))
					replacement = append(replacement, part)
				}
				offset += len(text)
			}
		}
		slice(0, edit.From)
		if edit.Insert != "" {
			replacement = append(replacement, map[string]any{"type": "text", "text": edit.Insert})
		}
		slice(edit.To, len(units))
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
				if !ok || len(list) > 1 {
					return fmt.Errorf("marks shape")
				}
				for _, value := range list {
					mark, ok := value.(map[string]any)
					if !ok {
						return fmt.Errorf("mark shape")
					}
					kind := mark["type"]
					if kind != "link" && kind != "code" {
						return fmt.Errorf("mark type")
					}
					for key := range mark {
						if key != "type" && key != "attrs" {
							return fmt.Errorf("mark field")
						}
					}
					if kind == "code" {
						if attrs, exists := mark["attrs"]; exists {
							m, ok := attrs.(map[string]any)
							if !ok || len(m) != 0 {
								return fmt.Errorf("code attrs")
							}
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
			case "paragraph", "table", "tableRow", "bulletList", "listItem", "codeBlock":
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
		if attrs, ok := node["attrs"].(map[string]any); ok {
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
