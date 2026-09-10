package pages

// Shared portable wire contract for Pages API and CLI. V2 adds a source draft;
// it never conveys execution authority or publication state.
const TransferV2 = "crewship-page-bundle/v2"
const MaxTransferBytes = MaxProjectDocumentBytes + MaxSpecBytes + (64 << 10)

type TransferBundle struct {
	Format  string              `json:"format" yaml:"format"`
	Page    TransferPage        `json:"page" yaml:"page"`
	Refs    []TransferReference `json:"references" yaml:"references"`
	Meta    TransferMetadata    `json:"metadata" yaml:"metadata"`
	Project *SourceProject      `json:"project,omitempty" yaml:"project,omitempty"`
}

type TransferPage struct {
	Name        string          `json:"name" yaml:"name"`
	Slug        string          `json:"slug" yaml:"slug"`
	Description string          `json:"description,omitempty" yaml:"description,omitempty"`
	Owner       string          `json:"owner,omitempty" yaml:"owner,omitempty"`
	Panels      []TransferPanel `json:"panels" yaml:"panels"`
}

type TransferPanel struct {
	ID         string          `json:"id" yaml:"id"`
	Schema     string          `json:"schema" yaml:"schema"`
	Title      string          `json:"title,omitempty" yaml:"title,omitempty"`
	Icon       string          `json:"icon,omitempty" yaml:"icon,omitempty"`
	Tab        string          `json:"tab,omitempty" yaml:"tab,omitempty"`
	Owner      string          `json:"owner" yaml:"owner"`
	Producer   string          `json:"producer" yaml:"producer"`
	SLASeconds int             `json:"sla_seconds" yaml:"sla_seconds"`
	Span       int             `json:"span,omitempty" yaml:"span,omitempty"`
	Actions    []PanelAction   `json:"actions,omitempty" yaml:"actions,omitempty"`
	Wake       []PanelWake     `json:"wake,omitempty" yaml:"wake,omitempty"`
	OnFailure  *PanelOnFailure `json:"on_failure,omitempty" yaml:"on_failure,omitempty"`
	Refresh    PanelRefresh    `json:"refresh,omitempty" yaml:"refresh,omitempty"`
}

type TransferReference struct {
	Ref      string   `json:"ref" yaml:"ref"`
	Kind     string   `json:"kind" yaml:"kind"`
	Bindable bool     `json:"bindable" yaml:"bindable"`
	UsedBy   []string `json:"used_by" yaml:"used_by"`
}

type TransferMetadata struct {
	ExportedAt string `json:"exported_at" yaml:"exported_at"`
	PanelCount int    `json:"panel_count" yaml:"panel_count"`
}

type TransferImport struct {
	Format string              `json:"format" yaml:"format"`
	Page   TransferPage        `json:"page" yaml:"page"`
	Refs   []TransferReference `json:"references" yaml:"references"`

	Slug    string            `json:"slug" yaml:"slug"`
	Bind    map[string]string `json:"bind" yaml:"bind"`
	Project *SourceProject    `json:"project,omitempty" yaml:"project,omitempty"`
	Meta    TransferMetadata  `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}
