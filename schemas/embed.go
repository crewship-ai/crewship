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
// a user-supplied string. Three shipped in v0 — metric, status and table —
// which is the set that needed no chart engine and therefore no new
// dependency; series and narrative followed, and neither added one either
// (the chart is hand-written inline SVG, the prose is React elements).
// embed.v1 followed as the sixth, and it is the one whose payload carries no
// content at all — only the NAME of a destination an operator already vetted.
//
// Two properties are shared by all of them and are load-bearing rather than
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

// PanelNarrativeV1 is the one an AI agent writes, so §8's ten rules are its
// specification. The three absences below are the rules, not a summary of
// them: no image field (rule 2 — CamoLeak came through a trusted first-party
// image proxy), no URL field (rule 3 — Slack AI's leak was a rendered link),
// and no markup field (rule 1 — the agent fills a schema, the host owns the
// look). Each is enforced by `additionalProperties: false` rather than by a
// sanitiser, because a sanitiser is a thing that can be got past.
//
//go:embed panel.narrative.v1.json
var PanelNarrativeV1 []byte

// PanelSeriesV1 carries §3's three chart rules in its shape: one `unit` for
// the whole panel and none on a series, so two units cannot be expressed; no
// `color` on a series, so colour stays a property of the entity that the
// renderer derives from the name; and a series count the server merges rather
// than refuses, so the five-colour bound never costs a producer a push.
//
//go:embed panel.series.v1.json
var PanelSeriesV1 []byte

// PanelEmbedV1 is the escape hatch (§3.1), and the shortest of the six because
// almost everything an embed panel could have carried is a field this schema
// refuses to have. It names one entry in the instance's vetted allow-list and
// one line of plain caption; it has no `url`, no `html`, no `srcdoc`, no
// `sandbox` and no geometry. An iframe src is fetched by the READER's browser,
// so a producer-settable URL would be the CamoLeak channel with execution
// added — §8 rules 1 to 3 keep it out of the schema rather than out of a
// payload. The reasoning is in internal/pages/embed.go.
//
//go:embed panel.embed.v1.json
var PanelEmbedV1 []byte
