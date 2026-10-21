package documents

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const CreationMaxBlocks = 128
const CreationMaxTextBytes = 256 * 1024
const CreationMaxItemBytes = 64 * 1024
const CreationMaxGeneratedBlocks = 512

// Match the authoring adapter's explicit Unicode whitespace plus BOM set.
const creationBlankCharacters = " \t\r\n\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

type CreationInput struct {
	Slug       string           `json:"slug"`
	Name       string           `json:"name"`
	MutationID string           `json:"mutation_id"`
	Blocks     []AuthoringBlock `json:"blocks"`
}

// AuthoringBlock contains text, not executable markup or CRDT bytes. Stable
// block identities and document hashes belong to the trusted engine.
type AuthoringBlock struct {
	Type  string   `json:"type"`
	Text  *string  `json:"text,omitempty"`
	Level *int     `json:"level,omitempty"`
	Items []string `json:"items,omitempty"`
}

func ValidateAuthoring(blocks []AuthoringBlock) error {
	if len(blocks) == 0 || len(blocks) > CreationMaxBlocks {
		return errors.New("one to 128 authoring blocks required")
	}
	total, generated := 0, 0
	readable := false
	text := func(value string) error {
		if !utf8.ValidString(value) || len(value) > CreationMaxItemBytes || len(value) > CreationMaxTextBytes-total {
			return errors.New("document authoring text exceeds limit or is invalid UTF-8")
		}
		for _, r := range value {
			if r < 32 && r != '\t' && r != '\n' && r != '\r' || r == 127 {
				return errors.New("document authoring text contains unsupported control characters")
			}
		}
		total += len(value)
		readable = readable || strings.Trim(value, creationBlankCharacters) != ""
		return nil
	}
	for _, block := range blocks {
		generated++
		switch block.Type {
		case "paragraph", "codeBlock", "heading":
			if block.Text == nil || block.Items != nil || (block.Type == "heading" && (block.Level == nil || *block.Level < 1 || *block.Level > 3)) || (block.Type != "heading" && block.Level != nil) {
				return errors.New("invalid text authoring block")
			}
			if err := text(*block.Text); err != nil {
				return err
			}
		case "bulletList", "orderedList":
			if block.Text != nil || block.Level != nil || len(block.Items) == 0 || len(block.Items) > 128 {
				return errors.New("invalid list authoring block")
			}
			generated += 2 * len(block.Items)
			for _, item := range block.Items {
				if err := text(item); err != nil {
					return err
				}
			}
		default:
			return errors.New("unsupported authoring block type")
		}
		if generated > CreationMaxGeneratedBlocks {
			return errors.New("too many generated document blocks")
		}
	}
	if !readable {
		return errors.New("document must contain readable content")
	}
	return nil
}
