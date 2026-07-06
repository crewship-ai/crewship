// Package schemas embeds the published routine JSON Schema so CLI
// commands and API handlers can serve it without a filesystem dependency
// (the binary ships self-contained). The schema is the machine-readable
// authoring contract that IDEs and `crewship routine schema` consume.
package schemas

import _ "embed"

// RoutineV1 is the JSON Schema (draft 2020-12) for a routine definition.
// Kept in sync with the pipeline DSL by TestRoutineSchema_* in
// internal/pipeline.
//
//go:embed routine.v1.json
var RoutineV1 []byte
