package api

type craftNativeDocumentQuotaResponse struct {
	UsedBytes  string `json:"usedBytes"`
	LimitBytes string `json:"limitBytes"`
	Warning    bool   `json:"warning"`
	Retention  string `json:"retention"`
}
type craftNativeDocumentResponse struct {
	Version       string                           `json:"version"`
	Bytes         []byte                           `json:"bytes"`
	ApplicationID string                           `json:"applicationId"`
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
