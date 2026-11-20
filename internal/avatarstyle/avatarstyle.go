// Package avatarstyle is the server's copy of the agent avatar style catalogue:
// the slugs the web app can draw (AVATAR_STYLES in lib/agent-avatar.ts).
//
// The server never draws an avatar — DiceBear is JavaScript — but it stores
// the style an agent or crew asks for, and a slug the web app does not know is
// silently drawn as the default. Checking it here turns that into a 400 for
// the API and a clear error in the CLI. TestSlugsMatchTheWebCatalogue keeps
// the two lists equal.
package avatarstyle

import "strings"

const (
	// Default is what an agent draws with when neither it nor its crew names a
	// style, and what new agents start on (#3789). DiceBear 10's Voxel Bot.
	Default = "voxel-bot"
	// Legacy is the default before #3789. Every agent that resolved to the old
	// default was pinned to it by migration, which is what keeps its face.
	Legacy = "bottts-neutral"
)

// slugs in the picker's order: the DiceBear 10 voxel styles first, then the
// 9.4 catalogue.
var slugs = []string{
	"voxel-bot", "voxel-art",
	"bottts-neutral", "bottts", "adventurer", "adventurer-neutral",
	"avataaars", "avataaars-neutral", "big-ears", "big-ears-neutral",
	"big-smile", "croodles", "croodles-neutral", "dylan", "fun-emoji",
	"lorelei", "lorelei-neutral", "micah", "miniavs", "notionists",
	"notionists-neutral", "open-peeps", "personas", "pixel-art",
	"pixel-art-neutral", "thumbs", "toon-head",
}

var known = func() map[string]bool {
	m := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		m[s] = true
	}
	return m
}()

// Valid reports whether style is a slug the web app can draw. The empty string
// is valid: it means "no style of its own" (follow the crew, else the default).
func Valid(style string) bool {
	return style == "" || known[style]
}

// Slugs returns the accepted slugs, in picker order. The slice is a copy.
func Slugs() []string {
	return append([]string(nil), slugs...)
}

// List is Slugs joined for help text and error messages.
func List() string {
	return strings.Join(slugs, ", ")
}

// Problem is the message for a refused slug, shared by the API and the CLI so
// both say the same thing.
func Problem(style string) string {
	return "unknown avatar style " + `"` + style + `"` + " (one of: " + List() + ")"
}

// Seed is the avatar seed a new agent is stored with: the one the caller
// chose, else the agent's slug (what the renderer falls back to anyway, so
// pinning it keeps the face if the slug is later changed). It deliberately
// does not touch the style: a NULL avatar_style is the documented "follow the
// crew's style, else Default" state, and crew style changes re-render only
// agents in that state (#3051). Every INSERT INTO agents goes through here.
func Seed(chosen *string, slug string) *string {
	if chosen != nil && strings.TrimSpace(*chosen) != "" {
		return chosen
	}
	return &slug
}
