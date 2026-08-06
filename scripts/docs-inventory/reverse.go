package main

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// reverseChecks is the docs → code half of the inventory. The existing API,
// CLI, environment, and manifest records prove code → docs; these counters
// prove that symbols introduced by documentation still exist in code.
type reverseChecks struct {
	CommandReferences int          `json:"command_references"`
	APIPathReferences int          `json:"api_path_references"`
	EnvReferences     int          `json:"environment_references"`
	KindReferences    int          `json:"manifest_kind_references"`
	FlagReferences    int          `json:"flag_references"`
	MissingCommands   int          `json:"missing_commands"`
	MissingAPIPaths   int          `json:"missing_api_paths"`
	MissingEnv        int          `json:"missing_environment_variables"`
	MissingKinds      int          `json:"missing_manifest_kinds"`
	MissingFlags      int          `json:"missing_flags"`
	Missing           []reverseRow `json:"missing,omitempty"`
}

type reverseRow struct {
	Kind   string `json:"kind"`
	Symbol string `json:"symbol"`
	Doc    string `json:"doc"`
	Line   int    `json:"line"`
}

func (r reverseChecks) missingRows(kind string) []string {
	var rows []string
	for _, row := range r.Missing {
		if row.Kind == kind {
			rows = append(rows, fmt.Sprintf("%s:%d: %s", row.Doc, row.Line, row.Symbol))
		}
	}
	return rows
}

var (
	docCommandPattern  = regexp.MustCompile("(?:^|[\\s`$>])crewship\\s+(.+)")
	docAPIPathPattern  = regexp.MustCompile(`/api/v1/[A-Za-z0-9_{}$./:~+-]+`)
	docFlagPattern     = regexp.MustCompile(`--[A-Za-z][A-Za-z0-9-]*`)
	docKindPattern     = regexp.MustCompile(`\bkind:\s*([A-Z][A-Za-z0-9_-]*)\b`)
	docIgnoreAPIPrefix = regexp.MustCompile(`docs-inventory: ignore-api-prefix\s+([^\s]+)`)
	docIgnoreAPI       = regexp.MustCompile(`docs-inventory: ignore-api\s+([^\s]+)`)
)

// inventoryDocsToCode scans executable-looking documentation contexts:
// fenced code blocks and inline code spans. Ordinary prose mentioning the
// product name is not a command invocation. A line containing
// "<!-- docs-inventory: ignore -->" is an explicit exception for placeholders
// or illustrative commands that intentionally are not product surfaces.
func inventoryDocsToCode(openAPI openAPIDocument, manifest commandManifest, docs []docFile, environment, manifestKinds []surfaceRecord) reverseChecks {
	commands, flags := commandInventory(manifest)
	apiPaths := make(map[string]bool, len(openAPI.Paths))
	for route := range openAPI.Paths {
		apiPaths[normalizeRoutePath(route)] = true
	}
	envs := make(map[string]bool, len(environment))
	for _, record := range environment {
		envs[record.Name] = true
	}
	kinds := make(map[string]bool, len(manifestKinds))
	for _, record := range manifestKinds {
		kinds[record.Name] = true
	}

	var result reverseChecks
	for _, doc := range docs {
		inFence := false
		lines := strings.Split(strings.ReplaceAll(doc.Text, "\r\n", "\n"), "\n")
		var ignoredAPIPrefixes []string
		var ignoredAPIPaths []string
		for _, match := range docIgnoreAPIPrefix.FindAllStringSubmatch(doc.Text, -1) {
			ignoredAPIPrefixes = append(ignoredAPIPrefixes, match[1])
		}
		for _, match := range docIgnoreAPI.FindAllStringSubmatch(doc.Text, -1) {
			ignoredAPIPaths = append(ignoredAPIPaths, match[1])
		}
		for lineNo, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				inFence = !inFence
				continue
			}
			if strings.Contains(line, "docs-inventory: ignore") {
				continue
			}
			contexts := docCodeContexts(line, inFence)
			for _, context := range contexts {
				result.scanCommands(doc, lineNo+1, context, commands, flags)
			}

			for _, raw := range docAPIPathPattern.FindAllString(line, -1) {
				ignored := false
				for _, prefix := range ignoredAPIPrefixes {
					if strings.HasPrefix(raw, prefix) || strings.HasPrefix(normalizeRoutePath(raw), normalizeRoutePath(prefix)) {
						ignored = true
						break
					}
				}
				for _, ignoredPath := range ignoredAPIPaths {
					if normalizeRoutePath(raw) == normalizeRoutePath(ignoredPath) {
						ignored = true
						break
					}
				}
				if ignored {
					continue
				}
				if !concreteDocAPIPath(raw, apiPaths) {
					continue
				}
				result.APIPathReferences++
				clean := canonicalDocPath(raw)
				if !apiPaths[normalizeRoutePath(clean)] && !equivalentAPIPath(normalizeRoutePath(clean), apiPaths) {
					result.MissingAPIPaths++
					result.Missing = append(result.Missing, reverseRow{Kind: "API path", Symbol: clean, Doc: doc.Path, Line: lineNo + 1})
				}
			}
			for _, name := range envNamePattern.FindAllString(line, -1) {
				result.EnvReferences++
				if !envs[name] {
					result.MissingEnv++
					result.Missing = append(result.Missing, reverseRow{Kind: "environment variable", Symbol: name, Doc: doc.Path, Line: lineNo + 1})
				}
			}
			for _, match := range docKindPattern.FindAllStringSubmatch(line, -1) {
				result.KindReferences++
				if !kinds[match[1]] {
					result.MissingKinds++
					result.Missing = append(result.Missing, reverseRow{Kind: "manifest kind", Symbol: match[1], Doc: doc.Path, Line: lineNo + 1})
				}
			}
		}
	}
	sort.Slice(result.Missing, func(i, j int) bool {
		if result.Missing[i].Doc != result.Missing[j].Doc {
			return result.Missing[i].Doc < result.Missing[j].Doc
		}
		return result.Missing[i].Line < result.Missing[j].Line
	})
	return result
}

func (r *reverseChecks) scanCommands(doc docFile, line int, context string, commands, flags map[string]bool) {
	match := docCommandPattern.FindStringSubmatch(context)
	if match == nil {
		return
	}
	r.CommandReferences++
	words := strings.Fields(match[1])
	var parts []string
	for _, word := range words {
		word = strings.Trim(word, "`\\,;|()[]{}")
		if word == "" || strings.HasPrefix(word, "-") || strings.ContainsAny(word, "<>=\"'") {
			break
		}
		parts = append(parts, word)
	}
	command := longestCommandPrefix(parts, commands)
	if command == "" {
		// A single unknown noun is still a useful reverse-gate signal (and
		// catches `crewship totally-made-up-commails`). Multi-word text is
		// commonly explanatory prose or command output; it must opt in with
		// the explicit ignore convention if it is intended as an invocation.
		if len(parts) == 1 {
			command = parts[0]
			r.MissingCommands++
			r.Missing = append(r.Missing, reverseRow{Kind: "command", Symbol: "crewship " + command, Doc: doc.Path, Line: line})
		}
	}
	for _, name := range docFlagPattern.FindAllString(context, -1) {
		r.FlagReferences++
		if !flags[strings.TrimPrefix(name, "--")] {
			r.MissingFlags++
			r.Missing = append(r.Missing, reverseRow{Kind: "flag", Symbol: name, Doc: doc.Path, Line: line})
		}
	}
}

func docCodeContexts(line string, inFence bool) []string {
	if inFence {
		return []string{line}
	}
	var contexts []string
	for remaining := line; ; {
		start := strings.IndexByte(remaining, '`')
		if start < 0 {
			break
		}
		remaining = remaining[start+1:]
		end := strings.IndexByte(remaining, '`')
		if end < 0 {
			break
		}
		contexts = append(contexts, remaining[:end])
		remaining = remaining[end+1:]
	}
	return contexts
}

func commandInventory(manifest commandManifest) (map[string]bool, map[string]bool) {
	commands := make(map[string]bool)
	flags := make(map[string]bool)
	for _, flag := range manifest.GlobalFlags {
		flags[flag.Name] = true
	}
	var walk func([]commandNode, string, []string)
	walk = func(nodes []commandNode, parent string, parentVariants []string) {
		for _, node := range nodes {
			full := node.Path
			if parent != "" && !strings.HasPrefix(full, parent+" ") {
				full = strings.TrimSpace(parent + " " + full)
			}
			variants := []string{full}
			for _, variant := range parentVariants {
				suffix := strings.TrimPrefix(full, parent)
				variants = append(variants, strings.TrimSpace(variant+suffix))
			}
			for _, alias := range node.Aliases {
				for _, variant := range append([]string{}, variants...) {
					parts := strings.Fields(variant)
					if len(parts) > 0 {
						parts[len(parts)-1] = alias
						variants = append(variants, strings.Join(parts, " "))
					}
				}
			}
			for _, variant := range variants {
				commands[variant] = true
			}
			for _, flag := range node.Flags {
				flags[flag.Name] = true
			}
			walk(node.Commands, full, variants)
		}
	}
	walk(manifest.Commands, "", nil)
	return commands, flags
}

func longestCommandPrefix(parts []string, commands map[string]bool) string {
	for n := len(parts); n > 0; n-- {
		candidate := strings.Join(parts[:n], " ")
		if commands[candidate] {
			return candidate
		}
	}
	return ""
}

func normalizeRoutePath(route string) string {
	route = canonicalDocPath(route)
	parts := strings.Split(strings.Trim(route, "/"), "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") || strings.HasPrefix(part, "$") || strings.HasPrefix(part, ":") || strings.HasPrefix(part, "<") {
			parts[i] = "{}"
		}
	}
	return path.Join(parts...)
}

func concreteDocAPIPath(raw string, apiPaths map[string]bool) bool {
	if strings.Contains(raw, "...") || strings.HasSuffix(raw, "/") {
		return false
	}
	normalized := normalizeRoutePath(raw)
	if apiPaths[normalized] || equivalentAPIPath(normalized, apiPaths) {
		return true
	}
	// Namespace references such as /api/v1/internal/ are explanatory prose,
	// not claims about a concrete operation. If a literal path is a prefix of
	// an inventoried operation, leave it to the operation-level documentation
	// checks rather than inventing a missing route.
	if !strings.Contains(raw, "{") && !strings.Contains(raw, "$") && !strings.Contains(raw, ":") && !strings.Contains(raw, "<") {
		prefix := strings.TrimSuffix(normalized, "/") + "/"
		for known := range apiPaths {
			if strings.HasPrefix(known, prefix) {
				return false
			}
		}
	}
	return true
}

func equivalentAPIPath(candidate string, apiPaths map[string]bool) bool {
	want := strings.Split(strings.Trim(candidate, "/"), "/")
	for known := range apiPaths {
		got := strings.Split(strings.Trim(known, "/"), "/")
		if len(want) != len(got) {
			continue
		}
		matches := true
		for i := range want {
			if want[i] != got[i] && want[i] != "{}" && got[i] != "{}" {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
