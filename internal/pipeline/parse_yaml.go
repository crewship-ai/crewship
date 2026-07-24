package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ToCanonicalJSON accepts either a JSON or a YAML routine DSL document and
// returns canonical JSON bytes suitable for Parse (#1423 item 2). JSON input
// (the historical, and still primary, format) passes through unchanged.
// YAML input is decoded and re-encoded as JSON — comments are dropped (YAML
// comments have no JSON equivalent, and were never persisted even when
// authors smuggled instructions into `description` as a workaround) and
// literal/folded block scalars (`prompt: |`) become real multiline JSON
// strings, no manual `\n` escaping required.
//
// This is the ONLY place format-sniffing happens. Every other Parse call
// site in the codebase (executor, API save handlers, DB reads) keeps
// receiving canonical JSON exactly as before — YAML is strictly an
// authoring-time convenience at the two CLI entry points that read a file
// off disk (`routine validate`, `routine save`), not a second on-disk or
// on-wire representation.
func ToCanonicalJSON(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, utf8BOM))
	if len(trimmed) == 0 {
		return nil, errors.New("pipeline: empty DSL document")
	}
	if looksLikeJSON(data) {
		// encoding/json (unlike yaml.v3) does not skip a leading BOM, so
		// the JSON pass-through path has to strip it explicitly — the
		// YAML branch below never sees this because yaml.Unmarshal(data,
		// ...) reads from the original (BOM-prefixed) bytes and yaml.v3
		// does handle it. Whitespace-only leading bytes are left as-is;
		// json.Unmarshal/json.Valid already tolerate those.
		return bytes.TrimPrefix(data, utf8BOM), nil
	}

	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, fmt.Errorf("pipeline: parse YAML DSL: %w", err)
	}
	out, err := json.Marshal(generic)
	if err != nil {
		// Unreachable in practice — yaml.v3 decodes mappings into
		// map[string]interface{} (string keys), which json.Marshal always
		// accepts. Kept as a named error rather than a panic so a future
		// yaml.v3 behavior change fails loudly instead of crashing the CLI.
		return nil, fmt.Errorf("pipeline: convert YAML DSL to JSON: %w", err)
	}
	return out, nil
}

// utf8BOM is the 3-byte UTF-8 byte-order mark some editors (notably on
// Windows) prepend to text files. Neither encoding/json nor yaml.v3 skips it
// on their own, so both looksLikeJSON's sniff and json.Valid downstream
// would otherwise see a leading garbage byte.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// looksLikeJSON sniffs whether data is JSON (vs YAML) by checking the first
// non-whitespace, non-BOM byte: JSON DSL documents always start with `{`
// (an object) — `[` is included too, even though a bare array is never a
// valid DSL document, because rejecting it is Parse/Validate's job, not the
// format sniffer's; misclassifying it as YAML would produce a confusing
// "yaml: ..." error for what's unambiguously JSON syntax.
func looksLikeJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, utf8BOM))
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '{', '[':
		return true
	default:
		return false
	}
}
