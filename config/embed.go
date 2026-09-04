// Package config embeds the hand-edited, reviewable configuration files that
// more than one side of Crewship reads. The files themselves are the source
// of truth; this package only makes them reachable from the binary without a
// filesystem dependency, the same way schemas/ does for the JSON Schemas.
package config

import _ "embed"

// ModelsJSON is the model catalog — the one list of models Crewship offers.
// internal/llm parses it into CuratedModels (the /api/v1/models fallback,
// `crewship model list`, the Guide's proposal validation); the web bundle
// imports the same file directly (lib/model-catalog.ts) for every picker.
// Before this file existed there were four lists that drifted apart: the Go
// curated map still offered gpt-4o and gemini-1.5 while the web offered
// GPT-5.5 and Gemini 2.5, and providerRuntimeDefaults named ids the
// validator then rejected.
//
//go:embed models.json
var ModelsJSON []byte
