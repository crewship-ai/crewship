package api

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// flowchart-v1 is an intentionally small grammar, not a sanitizer for arbitrary
// Mermaid. Conversion still runs only in the isolated, bounded browser worker.
// IDs are ASCII; labels contain Unicode letters/numbers, spaces and .,_()-.
var boardMermaidNodePattern = `([A-Za-z][A-Za-z0-9_]{0,63})(?:\[([\p{L}\p{N} .,_()-]{1,200})\])?`
var boardMermaidNode = regexp.MustCompile(`^` + boardMermaidNodePattern + `$`)
var boardMermaidEdge = regexp.MustCompile(`^` + boardMermaidNodePattern + `\s*(?:-->|---|-\.->|==>)\s*` + boardMermaidNodePattern + `$`)

func validateBoardMermaid(source string) (string, error) {
	if !utf8.ValidString(source) || len(source) > 32768 {
		return "", errors.New("Mermaid source exceeds 32 KiB UTF-8 limit")
	}
	source = strings.TrimSpace(strings.ReplaceAll(source, "\r\n", "\n"))
	lines := strings.Split(source, "\n")
	if len(lines) < 2 || !memberBoardString(strings.TrimSpace(lines[0]), "flowchart TD", "flowchart TB", "flowchart BT", "flowchart LR", "flowchart RL") {
		return "", errors.New("flowchart TD/TB/BT/LR/RL header required")
	}
	output := []string{strings.TrimSpace(lines[0])}
	nodes := map[string]string{}
	edges := 0
	addNode := func(id, label string) bool {
		if previous, exists := nodes[id]; exists && label != "" && previous != "" && previous != label {
			return false
		}
		if label != "" || nodes[id] == "" {
			nodes[id] = label
		}
		return len(nodes) <= 100
	}
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if len(line) > 2048 {
			return "", errors.New("Mermaid statement exceeds limit")
		}
		if match := boardMermaidEdge.FindStringSubmatch(line); match != nil {
			edges++
			if edges > 100 || !addNode(match[1], match[2]) || !addNode(match[3], match[4]) {
				return "", errors.New("Mermaid graph exceeds bounds or changes a node label")
			}
		} else if match := boardMermaidNode.FindStringSubmatch(line); match != nil {
			if !addNode(match[1], match[2]) {
				return "", errors.New("Mermaid graph exceeds bounds or changes a node label")
			}
		} else {
			return "", errors.New("unsupported Mermaid statement: use plain nodes and one flowchart edge per line")
		}
		output = append(output, line)
	}
	if len(nodes) == 0 {
		return "", errors.New("at least one Mermaid node required")
	}
	return strings.Join(output, "\n"), nil
}
func memberBoardString(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
