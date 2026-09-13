package api

// Pages folders — the icon and colour vocabularies (#2527).
//
// A folder wears an icon from the CREW icon set and a colour from the crew
// palette, chosen through the same picker a crew uses (analysis §1/1). Crews
// validate neither on the server: lib/crew-icons.ts resolves an unknown name
// to the first icon and lib/colors.ts an unknown colour to the default, so a
// bad value stored against a crew fails quietly on screen. A folder refuses
// both at save time by name instead, for the reason internal/pages/icons.go
// gives for panel icons: a glyph the client cannot draw looks like a design
// decision rather than an error, and the refusal that lists the set is the
// only place an author learns what the set is.
//
// Both lists are MIRRORS of the client's registries — CREW_ICONS in
// lib/crew-icons.ts, CREW_COLORS in lib/colors.ts — in the client's order, and
// TestPageFolderIcons_MirrorTheCrewIconRegistry reads those files and fails
// when either drifts. The client is the source of truth here, not this file:
// an icon is a glyph, and the glyph lives in the bundle.

import (
	"strconv"
	"strings"
)

// pageFolderIcons is CREW_ICONS by name, in registry order.
var pageFolderIcons = []string{
	"code", "shield", "megaphone", "chart", "users", "rocket",
	"brain", "palette", "briefcase", "globe", "zap", "heart",
	"database", "lock", "bug", "network", "bot", "package",
	"lightbulb", "wrench", "truck", "graduation", "phone", "send",
	"bell", "bookmark", "calendar", "camera", "clipboard", "cloud",
	"compass", "credit-card", "crown", "diamond", "flame", "gift",
	"headphones", "home", "key", "layers", "grid", "mail",
	"map", "monitor", "music", "newspaper", "pen", "printer",
	"radio", "scale", "search", "server", "settings", "cart",
	"star", "target", "terminal", "umbrella", "video", "wand",
	"wifi", "anchor", "aperture", "archive", "award", "banknote",
	"battery", "bike", "binary", "blocks", "bluetooth", "book-open",
	"box", "brush", "building", "building-2", "cable", "clapperboard",
	"clock", "cog", "container", "cookie", "cpu", "dice",
	"disc", "dollar", "dribbble", "drum", "eye", "factory",
	"feather", "file-text", "fingerprint", "flag", "folder", "gamepad",
	"gauge", "globe-2", "hand", "hash", "hard-drive", "image",
	"infinity", "joystick", "landmark", "languages", "leaf", "life-buoy",
	"link", "magnet", "message", "mic", "microscope", "mountain",
	"paint-bucket", "paperclip", "percent", "pill", "pizza", "plane",
	"plug", "power", "qr-code", "radar", "receipt", "repeat",
	"ruler", "sailboat", "scan", "scissors", "share", "shield-check",
	"shirt", "signal", "siren", "skull", "smartphone", "sparkles",
	"speaker", "stethoscope", "sun", "swords", "tag", "tent",
	"thumbs-up", "timer", "tornado", "trophy", "tv", "university",
	"unplug", "upload", "utensils", "wallet", "watch", "webcam",
	"wind", "activity", "airplay", "alarm", "apple", "atom",
	"axe", "baby", "backpack", "badge", "badge-check", "beaker",
	"bed", "beer", "bell-ring", "bird", "blinds", "bone",
	"book-marked", "brain-circuit", "brick-wall", "medical", "cake", "calculator",
	"car", "castle", "cat", "check-circle", "cherry", "church",
	"citrus", "cloud-rain", "cloud-sun", "clover", "code-xml", "coffee",
	"coins", "component", "construction", "contact", "croissant", "crosshair",
	"cuboid", "dog", "door", "dumbbell", "ear", "earth",
	"eclipse", "egg", "eraser", "fan", "fence", "ferris-wheel",
	"film", "fish", "flashlight", "flask", "flower", "footprints",
	"forklift", "frame", "fuel", "gem", "ghost", "glasses",
	"grape", "guitar", "hammer", "handshake", "hard-hat", "hop",
	"hospital", "hotel", "hourglass", "ice-cream", "inbox", "lamp",
	"lamp-desk", "laptop", "lasso", "dashboard", "library", "ligature",
	"list-music", "locate", "lollipop", "luggage", "map-pin", "martini",
	"medal", "milestone", "moon", "navigation", "nfc", "nut",
	"orbit", "paint-roller", "palm-tree", "party", "paw", "pc",
	"pencil", "piggy-bank", "pin", "takeoff", "play", "plug-zap",
	"podcast", "popcorn", "presentation", "puzzle", "rabbit", "rainbow",
	"rat", "recycle", "refresh", "refrigerator", "ribbon", "rotate-3d",
	"route", "rss", "satellite", "scaling", "school", "screen-share",
	"scroll", "shapes", "shell", "ship", "shopping-bag", "shovel",
	"shrub", "shuffle", "signature", "snail", "snowflake", "sofa",
	"soup", "spade", "sprout", "stamp", "store", "sunrise",
	"sword", "syringe", "table", "tablet", "telescope", "test-tube",
	"theater", "thermometer", "ticket", "traffic-cone", "train", "tree",
	"pine", "trending", "turtle", "type", "vault", "voicemail",
	"volume", "warehouse", "waves", "wheat", "wine", "workflow",
	"worm", "message-circle", "milk-off", "minus", "more", "mouse-pointer",
	"move", "panel", "replace", "trello", "triangle", "vibrate",
	"slice", "space", "star-half", "sunset", "tangent", "test-tubes",
	"square-stack", "swiss-franc", "utensils-crossed"}

// pageFolderColors is CREW_COLORS by key, in palette order.
var pageFolderColors = []string{
	"blue", "emerald", "violet", "amber", "rose", "cyan", "lime", "fuchsia",
}

var (
	pageFolderIconSet  = pageFolderNameSet(pageFolderIcons)
	pageFolderColorSet = pageFolderNameSet(pageFolderColors)
)

func pageFolderNameSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

// validPageFolderIcon accepts the empty string (no icon: the client draws its
// default folder glyph) and any name in the crew icon registry.
func validPageFolderIcon(icon string) bool { return icon == "" || pageFolderIconSet[icon] }

// validPageFolderColor accepts the empty string (no colour: the client's
// default) and any key in the crew palette.
func validPageFolderColor(color string) bool { return color == "" || pageFolderColorSet[color] }

// pageFolderColorRefusal names the whole palette, because eight words fit in
// an error and the caller has no other way to learn them.
func pageFolderColorRefusal(color string) string {
	return "color " + strings.TrimSpace(color) + " is not in the crew palette; use one of " +
		strings.Join(pageFolderColors, ", ") + ", or omit it"
}

// pageFolderIconRefusal names the count and the registry rather than 345
// words: the picker shows them, and the first few make the shape clear.
func pageFolderIconRefusal(icon string) string {
	return "icon " + strings.TrimSpace(icon) + " is not a crew icon; the set is the " +
		"crew icon registry (" + pageFolderIcons[0] + ", " + pageFolderIcons[1] + ", " +
		pageFolderIcons[2] + ", … " + strconv.Itoa(len(pageFolderIcons)) + " names) — the same picker a crew uses"
}
