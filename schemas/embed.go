// Package schemas embeds the published JSON Schemas so CLI commands and
// API handlers can serve them without a filesystem dependency (the binary
// ships self-contained). They are the machine-readable authoring contract
// that IDEs, producer scripts and `crewship routine schema` consume.
package schemas

import _ "embed"

// RoutineV1 is the JSON Schema (draft 2020-12) for a routine definition.
// Kept in sync with the pipeline DSL by TestRoutineSchema_* in
// internal/pipeline.
//
//go:embed routine.v1.json
var RoutineV1 []byte

// The panel payload schemas — layer 2 of the Pages format (PRD §6): a human
// writes the page spec in YAML, a machine writes the panel payload in JSON,
// and both are validated. There is no third DSL.
//
// The set of panel kinds is CLOSED (§3): a new one is a server release, never
// a user-supplied string. Three ship in v0 — metric, status and table — which
// is the set that needs no chart engine and therefore no new dependency.
// series.v1, narrative.v1 and embed.v1 are named in the migration's CHECK so
// admitting them later is additive, and they get their schema when they get
// their renderer.
//
// Two properties are shared by all three and are load-bearing rather than
// stylistic:
//
//   - `additionalProperties: false`, which is what stops a producer supplying
//     its own timestamp. Freshness is computed server-side from the timestamp
//     the server stored (§4 rule 2); a payload that could carry one would be a
//     panel that can claim to be current forever.
//   - no image, URL or markup field anywhere. Not sanitised — absent. Both
//     CamoLeak and the Slack AI incident exfiltrated private data through
//     content a reader's own client was happy to render (§8 rules 2 and 3).
//
// Kept in sync with the Go types by the tests in internal/pages.

//go:embed panel.metric.v1.json
var PanelMetricV1 []byte

//go:embed panel.status.v1.json
var PanelStatusV1 []byte

//go:embed panel.table.v1.json
var PanelTableV1 []byte
