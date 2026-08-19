package pages

// The panel vocabulary (PRD §3). Five schemas, closed, plus the one sandboxed
// escape hatch reserved for later (§3.1).
//
// Closed is the whole point. Spotify's HubFramework — a small set of primitive
// components composable into anything — was deprecated with the postmortem
// "maximum flexibility, minimum readability", and Grafana's open panel-plugin
// ecosystem produced 130 plugins in fifteen years. So a new panel kind is a
// server release, never a user-supplied string, and this type is where that
// rule is enforced rather than remembered.

// PanelSchema is the payload shape a panel declares.
type PanelSchema string

const (
	// SchemaMetric renders one number with an optional delta, target and
	// sparkline.
	SchemaMetric PanelSchema = "metric.v1"
	// SchemaSeries renders a grouped bar chart: one unit, shared labels, up to
	// five coloured series with a sixth merged into "other".
	SchemaSeries PanelSchema = "series.v1"
	// SchemaStatus renders a status grid; state carries a glyph and text, never
	// colour alone.
	SchemaStatus PanelSchema = "status.v1"
	// SchemaTable renders a table that collapses to a card list in a narrow
	// container.
	SchemaTable PanelSchema = "table.v1"
	// SchemaNarrative renders typed prose blocks and an optional verdict. Text
	// only: the action half is v1.1 (§12) and arrives with the PageAction
	// vocabulary and its server-verified click token, not with this payload.
	SchemaNarrative PanelSchema = "narrative.v1"
	// SchemaEmbed renders a cross-origin sandboxed iframe. Reserved for v1.2 —
	// the name exists from the first migration so admitting it later is
	// additive rather than a breaking change to a closed enum.
	SchemaEmbed PanelSchema = "embed.v1"
)

// producibleSchemas are the ones with a published payload schema and a
// renderer. Five of the six: everything except embed.v1, which needs a second
// origin and a sandbox proxy rather than a payload type (§3.1).
//
// series.v1 arrived without a chart engine. §9 proposed recharts, but under
// `output: "export"` its ResponsiveContainer measures with a client-side
// ResizeObserver and paints nothing until hydration, so the panel is
// hand-written inline SVG exactly as metric.v1's sparkline is — no new
// dependency, and the bars are in the exported HTML.
var producibleSchemas = map[PanelSchema]bool{
	SchemaMetric:    true,
	SchemaSeries:    true,
	SchemaStatus:    true,
	SchemaTable:     true,
	SchemaNarrative: true,
}

// knownSchemas is every name the schema column will accept, including the three
// reserved ones. Kept in step with the CHECK constraint in the pages migration:
// a name that is known here but not there fails at INSERT with a constraint
// error nobody can read.
var knownSchemas = map[PanelSchema]bool{
	SchemaMetric:    true,
	SchemaSeries:    true,
	SchemaStatus:    true,
	SchemaTable:     true,
	SchemaNarrative: true,
	SchemaEmbed:     true,
}

// Known reports whether s is a member of the closed set, reserved names
// included.
func (s PanelSchema) Known() bool { return knownSchemas[s] }

// Producible reports whether a producer may push a payload to a panel of this
// schema today. A reserved-but-unbuilt schema is Known and not Producible:
// accepting its payloads would fill the ring with data nothing can render.
//
// embed.v1 is the one whose answer is not a constant, and it is the only place
// in the vocabulary where that is true. The other five need a renderer, which
// either shipped or did not; embed.v1 additionally needs a set of destinations
// a human has vetted, and an instance with none has nothing a panel could be
// pointed at. So it is Known everywhere (a page carrying one is a valid page)
// and Producible only where CREWSHIP_PAGES_EMBED_SOURCES declares something —
// which also means a page spec cannot declare an embed panel on an instance
// that could only draw it as a refusal.
func (s PanelSchema) Producible() bool {
	if s == SchemaEmbed {
		return EmbedEnabled()
	}
	return producibleSchemas[s]
}

func (s PanelSchema) String() string { return string(s) }

// ProducerKind is who is permitted to write a panel's payload (§7.1 rule 4).
// Producer authority is separate from viewer authority: a crew member who can
// SEE a panel cannot WRITE it.
//
// There is no 'sql' or 'datasource' member, and there never will be. A page
// holds no query and no credentials — everything a client wants on a page is
// reachable because the producer already runs next to that data inside a crew
// container. Adding a data source is a scripting job, not a connector
// engineering job, and it stays that way only if this set stays closed.
type ProducerKind string

const (
	// ProducerRoutine — a routine run pushes the payload; its run id becomes
	// the panel's provenance.
	ProducerRoutine ProducerKind = "routine"
	// ProducerScript — a script inside a crew container pushes through the
	// CLI or the sidecar.
	ProducerScript ProducerKind = "script"
	// ProducerAgent — an agent holding a `produce` grant on this panel.
	ProducerAgent ProducerKind = "agent"
	// ProducerWebhook — an inbound token bound to exactly one panel, for a
	// producer that cannot run the CLI (a cron on someone else's box, a CI
	// step, a gateway). A leaked token writes one panel and nothing else.
	ProducerWebhook ProducerKind = "webhook"
)

var knownProducerKinds = map[ProducerKind]bool{
	ProducerRoutine: true,
	ProducerScript:  true,
	ProducerAgent:   true,
	ProducerWebhook: true,
}

// Known reports whether k is a member of the closed set.
func (k ProducerKind) Known() bool { return knownProducerKinds[k] }

func (k ProducerKind) String() string { return string(k) }
