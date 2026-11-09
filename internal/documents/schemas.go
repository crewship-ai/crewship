package documents

// Content schemas and what each one supports (#5233). Every gate on a
// document's schema asks this table instead of comparing with one number or
// with LatestSchema, so a new schema can never silently switch a capability
// on or off for documents that already exist. SCHEMA_CAPABILITIES in
// tools/docs-service/schema-core.mjs mirrors it for the engine and browser;
// a test keeps them equal.

// CreateSchema is the schema new Docs are born at, from the New Doc dialog,
// the Artifacts library and an agent's doc create. It is explicit: raising
// LatestSchema never changes it, and it must have every agent capability.
const CreateSchema = 5

// MarksSchema is schema 5: underline, colours, highlights, dates and
// mentions. From it on a restore never lowers a Document's schema.
const MarksSchema = 5

// CanvasSchema is schema 6: schema 5 plus the Docs Canvas surface.
const CanvasSchema = 6

// FormattedSchema is schema 4: bold, italic, strike, code block languages,
// quotes and task lists. It is the oldest schema agents propose on.
const FormattedSchema = 4

// Capability is one thing a document of a given schema supports.
type Capability string

const (
	// CapRead: the content loads, validates and hashes.
	CapRead Capability = "read"
	// CapEdit: a person edits it in an editor that knows the schema.
	CapEdit Capability = "edit"
	// CapDraft: an agent run with creation consent drafts on it directly.
	CapDraft Capability = "draft"
	// CapPropose: an agent run with a propose mandate stores a proposal.
	CapPropose Capability = "propose"
	// CapAccept: a person accepts a proposal made at this same schema.
	CapAccept Capability = "accept"
	// CapRestore: a retained checkpoint is restored as a new revision.
	CapRestore Capability = "restore"
	// CapExport: Markdown and Word export.
	CapExport Capability = "export"
	// CapUpgrade: a person explicitly upgrades it to LatestSchema.
	CapUpgrade Capability = "upgrade"
)

// capabilityOrder is the stable wire order.
var capabilityOrder = []Capability{CapRead, CapEdit, CapDraft, CapPropose, CapAccept, CapRestore, CapExport, CapUpgrade}

var schemaCapabilities = map[int]map[Capability]bool{
	1: {CapRead: true, CapEdit: true, CapRestore: true, CapExport: true, CapUpgrade: true},
	2: {CapRead: true, CapEdit: true, CapRestore: true, CapExport: true, CapUpgrade: true},
	// Schema 3's agent path is the direct draft; its mandate never proposes.
	3: {CapRead: true, CapEdit: true, CapDraft: true, CapRestore: true, CapExport: true, CapUpgrade: true},
	4: {CapRead: true, CapEdit: true, CapDraft: true, CapPropose: true, CapAccept: true, CapRestore: true, CapExport: true, CapUpgrade: true},
	5: {CapRead: true, CapEdit: true, CapDraft: true, CapPropose: true, CapAccept: true, CapRestore: true, CapExport: true, CapUpgrade: true},
	// Schema 6 is schema 5 plus the Canvas surface. It keeps every capability
	// schema 5 released: agents propose on Page blocks and never write the
	// surface in v1, so enabling Canvas never takes the agent pipeline away.
	6: {CapRead: true, CapEdit: true, CapDraft: true, CapPropose: true, CapAccept: true, CapRestore: true, CapExport: true},
}

// SchemaAllows reports whether documents of this schema support c. An
// unknown schema supports nothing, so it fails closed.
func SchemaAllows(schema int, c Capability) bool { return schemaCapabilities[schema][c] }

// SchemaCapabilities lists what this schema supports, in a stable order.
func SchemaCapabilities(schema int) []string {
	out := []string{}
	for _, c := range capabilityOrder {
		if SchemaAllows(schema, c) {
			out = append(out, string(c))
		}
	}
	return out
}
