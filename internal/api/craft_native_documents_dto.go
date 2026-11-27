package api

type craftNativeDocumentQuotaResponse struct {
	UsedBytes  string `json:"usedBytes"`
	LimitBytes string `json:"limitBytes"`
	Warning    bool   `json:"warning"`
	Level      string `json:"level,omitempty"`
	Retention  string `json:"retention"`
}
type craftNativeDocumentResponse struct {
	Version       string                           `json:"version"`
	LegacySource  bool                             `json:"legacySource,omitempty"`
	Format        string                           `json:"format,omitempty"`
	Bytes         []byte                           `json:"bytes"`
	ApplicationID string                           `json:"applicationId"`
	EngineBuild   string                           `json:"engineBuild,omitempty"`
	EnginePin     string                           `json:"enginePin"`
	EditorBundle  string                           `json:"editorBundle"`
	EditorURL     string                           `json:"editorURL"`
	Quota         craftNativeDocumentQuotaResponse `json:"quota"`
}
type craftNativeRestoreResponse struct {
	Version    string `json:"version"`
	Durability string `json:"durability"`
	IssuedBy   string `json:"issuedBy"`
	Phase      string `json:"phase"`
}
