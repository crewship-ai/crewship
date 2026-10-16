// Package pagesearch derives bounded plain-text projections from validated
// native authority. Projections never authorize access or replace source bytes.
package pagesearch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"unicode"
	"unicode/utf8"
)

const (
	AdapterVersion     = 2
	MaxQueryBytes      = 256
	MaxQueryTerms      = 32
	MaxTextBytes       = 2 << 20
	MaxProjectionBytes = 4 << 20
	MaxSegments        = 20000
	MaxSnippetBytes    = 512
)

var (
	ErrUnsupported = errors.New("unsupported native search schema")
	ErrInvalid     = errors.New("invalid native search projection")
	ErrBudget      = errors.New("native search projection budget exceeded")
	identifier     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	digest         = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// SourceTag is a necessary content fence, not an authorization capability.
// Docs Hash covers the complete persisted ID/hash projection; generation
// changes on restore. Slides Hash is the native canonical Deck hash.
type SourceTag struct {
	WorkspaceID string `json:"workspace_id"`
	PageID      string `json:"page_id"`
	Type        string `json:"type"`
	Schema      int    `json:"schema"`
	Revision    int64  `json:"revision"`
	Generation  int64  `json:"generation"`
	Hash        string `json:"hash"`
}

func (s SourceTag) Validate() error {
	if !identifier.MatchString(s.WorkspaceID) || !identifier.MatchString(s.PageID) || s.Revision < 0 || s.Generation < 0 || !digest.MatchString(s.Hash) {
		return ErrInvalid
	}
	switch s.Type {
	case "document":
		if s.Schema < 1 || s.Schema > 3 {
			return ErrUnsupported
		}
	case "slides":
		if (s.Schema != 1 && s.Schema != 2) || s.Generation != 0 || s.Revision < 1 {
			return ErrUnsupported
		}
	default:
		return ErrUnsupported
	}
	return nil
}

// Current compares content identity only. Callers must separately recheck exact
// credentials and live object authority in the same final read snapshot.
func (s SourceTag) Current(current SourceTag) bool {
	return s.Validate() == nil && current.Validate() == nil && s == current
}

type Segment struct {
	Kind     string `json:"kind"`
	TargetID string `json:"target_id"`
	ParentID string `json:"parent_id,omitempty"`
	Text     string `json:"text"`
}

type Projection struct {
	AdapterVersion int       `json:"adapter_version"`
	Source         SourceTag `json:"source"`
	Segments       []Segment `json:"segments"`
}

func validText(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func (p Projection) Validate() error {
	if p.AdapterVersion != AdapterVersion {
		return ErrUnsupported
	}
	if err := p.Source.Validate(); err != nil {
		return err
	}
	if p.Segments == nil {
		return ErrInvalid
	}
	if len(p.Segments) > MaxSegments {
		return ErrBudget
	}
	total := 0
	for _, segment := range p.Segments {
		if !identifier.MatchString(segment.TargetID) || segment.ParentID != "" && !identifier.MatchString(segment.ParentID) || !validText(segment.Text) {
			return ErrInvalid
		}
		validKind := p.Source.Type == "document" && (segment.Kind == "paragraph" || segment.Kind == "heading" || p.Source.Schema == 3 && segment.Kind == "codeBlock") || p.Source.Type == "slides" && (segment.Kind == "text" || segment.Kind == "table-cell" || segment.Kind == "notes")
		if !validKind {
			return ErrUnsupported
		}
		if len(segment.Text) > MaxTextBytes-total {
			return ErrBudget
		}
		total += len(segment.Text)
	}
	return nil
}

// Encode hashes the exact bounded derived encoding. Readers must verify both
// encoded digest and current source tag before admitting cached text.
func (p Projection) Encode() ([]byte, string, error) {
	if err := p.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > MaxProjectionBytes {
		return nil, "", ErrBudget
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

type BlockHash struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

// DocumentHash binds the complete persisted projection, independent of row
// ordering. A caller cannot substitute only the matching leaf subset.
func DocumentHash(blocks []BlockHash) (string, error) {
	if len(blocks) > MaxSegments {
		return "", ErrBudget
	}
	ordered := append([]BlockHash{}, blocks...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for i, block := range ordered {
		if !identifier.MatchString(block.ID) || !digest.MatchString(block.Hash) || i > 0 && ordered[i-1].ID == block.ID {
			return "", ErrInvalid
		}
	}
	raw, err := json.Marshal(ordered)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("crewship-document-search-source/v1\n"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

// Decode admits only our exact versioned encoding, with its independent stored
// digest. Canonical roundtrip rejects duplicate/unknown fields and malformed
// source tags even when corrupted data carries a recomputed digest.
func Decode(raw []byte, expectedHash string) (Projection, error) {
	var projection Projection
	if len(raw) > MaxProjectionBytes {
		return projection, ErrBudget
	}
	if !utf8.Valid(raw) || !digest.MatchString(expectedHash) {
		return projection, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expectedHash {
		return projection, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&projection); err != nil {
		return projection, ErrInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return projection, ErrInvalid
	}
	encoded, _, err := projection.Encode()
	if err != nil {
		return projection, err
	}
	if !bytes.Equal(raw, encoded) {
		return projection, ErrInvalid
	}
	return projection, nil
}
