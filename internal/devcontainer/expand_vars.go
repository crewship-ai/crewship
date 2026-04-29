package devcontainer

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ExpandVars replaces devcontainer.json variable references in the input
// string. Currently supports:
//
//   - ${devcontainerId} — a stable, Docker-volume-safe identifier derived
//     from the crew ID (alphanumeric only, max 32 chars). Required by any
//     feature that wants per-container persistent volumes.
//
// crewID is the canonical crew identifier (e.g. "cmoiqveyn000bc572009a").
// It must be non-empty for ${devcontainerId} expansion; if empty, the
// variable is left as-is so the caller can detect misuse.
//
// Volume names per Docker rules: [a-zA-Z0-9][a-zA-Z0-9_.-]+. We hash the
// crewID with SHA-256 and take the first 16 hex chars to guarantee
// safe characters and bounded length even for crew IDs that happen to
// contain non-volume-safe runes.
func ExpandVars(input, crewID string) string {
	if input == "" {
		return input
	}
	if crewID != "" && strings.Contains(input, "${devcontainerId}") {
		input = strings.ReplaceAll(input, "${devcontainerId}", devcontainerIDFor(crewID))
	}
	return input
}

// devcontainerIDFor returns a deterministic Docker-volume-safe ID for
// a given crew. SHA-256 truncated to 16 hex chars (~64 bits of state)
// — collision probability across realistic crew counts is negligible.
func devcontainerIDFor(crewID string) string {
	sum := sha256.Sum256([]byte(crewID))
	return hex.EncodeToString(sum[:8])
}
