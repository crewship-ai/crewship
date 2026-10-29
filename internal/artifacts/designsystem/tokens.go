// Package designsystem validates a bounded literal token profile. It is not a
// design-system artifact type, resolver or store. Persist source files and pins
// through the existing artifact VersionStore; callers still enforce their ACLs.
package designsystem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Profile limits bound parsing and the amount of literal style data a consumer
// may receive. Version 1 supports neither aliases nor fonts or imports.
const (
	MaxProfileBytes   = 32 * 1024
	MaxTokensPerGroup = 128
	MaxNameBytes      = 48
	MaxSpacingPX      = 4096
)

// Snapshot contains normalized literal values and their canonical identity.
// Its fields are private and every returned collection is a detached copy.
// Consumers can materialize these hex colors and integer pixels without adding
// expressions, network references or exceptions to their own style validators.
type Snapshot struct {
	colors    map[string]string
	spacing   map[string]int
	canonical []byte
	digest    string
}

// CanonicalJSON returns compact schema-1 JSON with lowercase hex colors,
// lexicographically sorted token names, and decimal integer spacing values.
func (s *Snapshot) CanonicalJSON() []byte { return bytes.Clone(s.canonical) }

// SHA256 returns the lowercase hex digest of CanonicalJSON, not the source bytes.
// It does not establish a VersionStore source file hash, page identity or version
// pin. A provenance record must retain those independent source identifiers.
func (s *Snapshot) SHA256() string { return s.digest }

// Colors returns a detached map of literal #rrggbb values.
func (s *Snapshot) Colors() map[string]string {
	result := make(map[string]string, len(s.colors))
	for name, value := range s.colors {
		result[name] = value
	}
	return result
}

// Spacing returns a detached map of nonnegative integer pixel values.
func (s *Snapshot) Spacing() map[string]int {
	result := make(map[string]int, len(s.spacing))
	for name, value := range s.spacing {
		result[name] = value
	}
	return result
}

// Parse accepts exactly {schema_version:1,colors:{name:"#RRGGBB"},spacing:{name:px}}.
// All three fields are required; empty groups are valid. Names are lowercase
// ASCII identifiers starting with a letter, followed by letters, digits or '-'.
// Reserved prototype names, duplicate decoded keys, unsupported fields, invalid
// UTF-8, non-integer number spellings and trailing input fail closed. Errors do
// not echo token values, which may come from untrusted source files.
func Parse(source []byte) (*Snapshot, error) {
	if len(source) > MaxProfileBytes || !utf8.Valid(source) {
		return nil, errors.New("invalid token profile size or encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	if err := expectObject(decoder); err != nil {
		return nil, err
	}
	snapshot := &Snapshot{colors: make(map[string]string), spacing: make(map[string]int)}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		name, err := readKey(decoder, seen)
		if err != nil {
			return nil, err
		}
		switch name {
		case "schema_version":
			value, err := decoder.Token()
			if err != nil || value != json.Number("1") {
				return nil, errors.New("unsupported token schema version")
			}
		case "colors":
			if err := parseGroup(decoder, func(name string, value json.Token) error {
				color, ok := value.(string)
				if !ok || !validColor(color) {
					return errors.New("invalid literal color")
				}
				snapshot.colors[name] = strings.ToLower(color)
				return nil
			}); err != nil {
				return nil, err
			}
		case "spacing":
			if err := parseGroup(decoder, func(name string, value json.Token) error {
				number, ok := value.(json.Number)
				if !ok {
					return errors.New("invalid literal spacing")
				}
				spelling := number.String()
				if spelling == "" || strings.IndexFunc(spelling, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
					return errors.New("spacing must be a nonnegative decimal integer")
				}
				px, err := strconv.Atoi(spelling)
				if err != nil || px > MaxSpacingPX {
					return errors.New("spacing exceeds profile limit")
				}
				snapshot.spacing[name] = px
				return nil
			}); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("unsupported token profile field")
		}
	}
	if err := expectEnd(decoder); err != nil {
		return nil, err
	}
	if len(seen) != 3 {
		return nil, errors.New("missing token profile field")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing token profile input")
	}
	snapshot.canonical = canonical(snapshot)
	sum := sha256.Sum256(snapshot.canonical)
	snapshot.digest = hex.EncodeToString(sum[:])
	return snapshot, nil
}

func expectObject(decoder *json.Decoder) error {
	value, err := decoder.Token()
	if err != nil || value != json.Delim('{') {
		return errors.New("expected token object")
	}
	return nil
}

func expectEnd(decoder *json.Decoder) error {
	value, err := decoder.Token()
	if err != nil || value != json.Delim('}') {
		return errors.New("invalid token object")
	}
	return nil
}

func readKey(decoder *json.Decoder, seen map[string]bool) (string, error) {
	value, err := decoder.Token()
	if err != nil {
		return "", errors.New("invalid token key")
	}
	name, ok := value.(string)
	if !ok || seen[name] {
		return "", errors.New("invalid or duplicate token key")
	}
	seen[name] = true
	return name, nil
}

func parseGroup(decoder *json.Decoder, accept func(string, json.Token) error) error {
	if err := expectObject(decoder); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for decoder.More() {
		name, err := readKey(decoder, seen)
		if err != nil {
			return err
		}
		if len(seen) > MaxTokensPerGroup || !validName(name) {
			return errors.New("invalid token name or count")
		}
		value, err := decoder.Token()
		if err != nil {
			return errors.New("invalid token literal")
		}
		if err := accept(name, value); err != nil {
			return err
		}
	}
	return expectEnd(decoder)
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > MaxNameBytes || name == "constructor" || name == "prototype" {
		return false
	}
	for i, c := range []byte(name) {
		if c >= 'a' && c <= 'z' {
			continue
		}
		if i > 0 && ((c >= '0' && c <= '9') || c == '-') {
			continue
		}
		return false
	}
	return true
}

func validColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	for _, c := range color[1:] {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// Names and colors have already been constrained to printable ASCII, so this
// canonical encoding cannot introduce quoting ambiguity or HTML/CSS syntax.
func canonical(snapshot *Snapshot) []byte {
	var output strings.Builder
	output.WriteString(`{"schema_version":1,"colors":{`)
	names := make([]string, 0, len(snapshot.colors))
	for name := range snapshot.colors {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		if i > 0 {
			output.WriteByte(',')
		}
		fmt.Fprintf(&output, `"%s":"%s"`, name, snapshot.colors[name])
	}
	output.WriteString(`},"spacing":{`)
	names = names[:0]
	for name := range snapshot.spacing {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		if i > 0 {
			output.WriteByte(',')
		}
		fmt.Fprintf(&output, `"%s":%d`, name, snapshot.spacing[name])
	}
	output.WriteString(`}}`)
	return []byte(output.String())
}
