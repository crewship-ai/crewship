package api

type documentSemanticEdit struct {
	From   int    `json:"from"`
	To     int    `json:"to"`
	Insert string `json:"insert"`
}

type documentSemanticPatch struct {
	Target string                `json:"target"`
	Text   string                `json:"text"`
	Edit   *documentSemanticEdit `json:"edit"`
	Rich   *documentRichProof    `json:"rich,omitempty"`
}
