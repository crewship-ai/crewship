// Package kindguide is the registry of versioned agent guides, one per
// Artifact kind. A guide tells an agent working on an artifact of that kind
// the exact data shapes and limits, the write order, the read-before-write
// version check, the conflict rule, the agent modes and when not to verify.
//
// Guides are static product text embedded in the binary. They describe what
// the server already enforces; they never enable a feature, grant access or
// widen an agent's mode. The shared rules (guides/rules.md) are appended to
// every kind's body so the owner's fixed decisions cannot drift per kind.
package kindguide

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxGuideBytes bounds one served guide (kind body plus shared rules), so a
// guide always fits an agent context and a tool response.
const MaxGuideBytes = 16 * 1024

// ErrUnknownKind is returned for a kind without a guide, including the Craft
// kinds (word, sheet), which follow the crewship-craft playbook instead.
var ErrUnknownKind = errors.New("unknown artifact kind")

//go:embed guides/*.md
var files embed.FS

// order is the served order: the Artifact family as the UI lists it, with
// Library (a Design whose content is a design system) after Design.
var order = []string{"docs", "slides", "design", "library", "dashboard", "app"}

// aliases maps storage and UI words agents meet to a guide kind.
var aliases = map[string]string{
	"doc": "docs", "document": "docs",
	"deck": "slides", "slide": "slides",
	"design-system": "library", "design_system": "library",
	"project": "app", "application": "app",
}

// Guide is one kind's served guide.
type Guide struct {
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Version      string `json:"version"`
	RulesVersion string `json:"rules_version"`
	SHA256       string `json:"sha256"`
	Bytes        int    `json:"bytes"`
	// Modes are the agent modes this kind has a path for. A listed mode is
	// still subject to the caller's live access on every call.
	Modes        []string     `json:"modes"`
	Capabilities Capabilities `json:"capabilities"`
	Content      string       `json:"content"`
}

// Capabilities declares what the agent profile of a kind supports and, by
// name, what it does not, so a client hides an unsupported feature instead
// of imitating it. The declaration grants nothing.
type Capabilities struct {
	Supported   []string `json:"supported" yaml:"supported"`
	Unsupported []string `json:"unsupported" yaml:"unsupported"`
}

// AgentModes is the closed mode vocabulary, in increasing authority.
var AgentModes = []string{"ask", "comment", "propose", "edit"}

type frontMatter struct {
	Kind         string       `yaml:"kind"`
	Title        string       `yaml:"title"`
	Version      string       `yaml:"version"`
	Modes        []string     `yaml:"modes"`
	Capabilities Capabilities `yaml:"capabilities"`
}

var registry = mustLoad()

func mustLoad() map[string]Guide {
	rulesMeta, rules, err := parse("guides/rules.md")
	if err != nil {
		panic(err)
	}
	out := make(map[string]Guide, len(order))
	for _, kind := range order {
		meta, body, err := parse("guides/" + kind + ".md")
		if err != nil {
			panic(err)
		}
		if meta.Kind != kind {
			panic(fmt.Sprintf("kindguide: guides/%s.md declares kind %q", kind, meta.Kind))
		}
		content := strings.TrimRight(body, "\n") + "\n\n" + rules
		sum := sha256.Sum256([]byte(content))
		out[kind] = Guide{
			Kind: kind, Title: meta.Title, Version: meta.Version, RulesVersion: rulesMeta.Version,
			SHA256: hex.EncodeToString(sum[:]), Bytes: len(content), Content: content,
			Modes: meta.Modes, Capabilities: meta.Capabilities,
		}
	}
	return out
}

func parse(name string) (frontMatter, string, error) {
	raw, err := files.ReadFile(name)
	if err != nil {
		return frontMatter{}, "", err
	}
	rest, ok := bytes.CutPrefix(raw, []byte("---\n"))
	if !ok {
		return frontMatter{}, "", fmt.Errorf("kindguide: %s has no front matter", name)
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return frontMatter{}, "", fmt.Errorf("kindguide: %s front matter is not closed", name)
	}
	var meta frontMatter
	decoder := yaml.NewDecoder(bytes.NewReader(head))
	decoder.KnownFields(true)
	if err := decoder.Decode(&meta); err != nil || meta.Title == "" || meta.Version == "" {
		return frontMatter{}, "", fmt.Errorf("kindguide: %s front matter invalid", name)
	}
	return meta, strings.TrimLeft(string(body), "\n"), nil
}

// Normalize maps a kind or a known alias to a guide kind.
func Normalize(kind string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	if len(k) > 32 {
		return "", ErrUnknownKind
	}
	if alias, ok := aliases[k]; ok {
		k = alias
	}
	if _, ok := registry[k]; !ok {
		return "", ErrUnknownKind
	}
	return k, nil
}

// Lookup returns the guide for a kind or alias.
func Lookup(kind string) (Guide, error) {
	k, err := Normalize(kind)
	if err != nil {
		return Guide{}, err
	}
	return detached(registry[k]), nil
}

// Kinds returns the guide kinds in served order.
func Kinds() []string { return append([]string(nil), order...) }

// List returns every guide in served order. Slices are copies.
func List() []Guide {
	out := make([]Guide, 0, len(order))
	for _, kind := range order {
		out = append(out, detached(registry[kind]))
	}
	return out
}

func detached(g Guide) Guide {
	g.Modes = append([]string{}, g.Modes...)
	g.Capabilities.Supported = append([]string{}, g.Capabilities.Supported...)
	g.Capabilities.Unsupported = append([]string{}, g.Capabilities.Unsupported...)
	return g
}
