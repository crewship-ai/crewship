package memport

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// frontmatter is the subset of OKF's YAML header this package reads or
// writes. Unknown keys in a source bundle are preserved by nothing —
// they are metadata for a system that is not us, and inventing storage
// for them would be a schema we cannot honour.
//
// CrewshipPath is our own extension. An OKF bundle we exported records
// where each file came from so re-importing it is exact rather than
// heuristic; a foreign bundle simply will not have the key.
type frontmatter struct {
	Type         string   `yaml:"type,omitempty"`
	Title        string   `yaml:"title,omitempty"`
	Tags         []string `yaml:"tags,omitempty"`
	CrewshipPath string   `yaml:"crewship_path,omitempty"`
}

var fmDelim = []byte("---")

// splitFrontmatter separates a YAML frontmatter block from the markdown
// body. ok is false when the file does not open with a delimiter line,
// in which case body is the whole input — a plain markdown file is not
// an error, it is just a file without metadata.
func splitFrontmatter(b []byte) (meta, body []byte, ok bool) {
	trimmed := bytes.TrimPrefix(b, []byte("\ufeff")) // strip a UTF-8 BOM if present
	if !bytes.HasPrefix(trimmed, fmDelim) {
		return nil, b, false
	}
	// The opening delimiter must be its own line.
	rest := trimmed[len(fmDelim):]
	rest = bytes.TrimPrefix(rest, []byte("\r"))
	if !bytes.HasPrefix(rest, []byte("\n")) {
		return nil, b, false
	}
	rest = rest[1:]

	// Find the closing delimiter at the start of a line.
	idx := indexLinePrefix(rest, fmDelim)
	if idx < 0 {
		// An unterminated header is not frontmatter; treating the
		// remainder of the file as YAML would swallow the content.
		return nil, b, false
	}
	meta = rest[:idx]
	after := rest[idx+len(fmDelim):]
	// Drop the remainder of the delimiter line.
	if nl := bytes.IndexByte(after, '\n'); nl >= 0 {
		after = after[nl+1:]
	} else {
		after = nil
	}
	return meta, after, true
}

// indexLinePrefix finds the offset of the first line in b that begins
// with prefix, or -1.
func indexLinePrefix(b, prefix []byte) int {
	for off := 0; off < len(b); {
		if bytes.HasPrefix(b[off:], prefix) {
			return off
		}
		nl := bytes.IndexByte(b[off:], '\n')
		if nl < 0 {
			return -1
		}
		off += nl + 1
	}
	return -1
}

// parseFrontmatter reads the header of an OKF document. A header that
// does not parse is reported: silently importing a file whose metadata
// we could not read would put it in the wrong tier.
func parseFrontmatter(b []byte) (frontmatter, []byte, error) {
	meta, body, ok := splitFrontmatter(b)
	if !ok {
		return frontmatter{}, body, nil
	}
	var fm frontmatter
	if err := yaml.Unmarshal(meta, &fm); err != nil {
		return frontmatter{}, body, err
	}
	return fm, body, nil
}

// renderFrontmatter emits a document with a YAML header. Field order is
// fixed by the struct, so exporting the same memory twice produces
// byte-identical output and a bundle kept in git shows real changes
// rather than key reshuffling.
func renderFrontmatter(fm frontmatter, body []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(fmDelim)
	buf.WriteByte('\n')
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	buf.Write(fmDelim)
	buf.WriteString("\n\n")
	buf.Write(bytes.TrimLeft(body, "\n"))
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}
