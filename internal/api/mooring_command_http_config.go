package api

import "net/http"

// MooringCommandHTTPConfig is trusted startup configuration for NEW native
// command documents only. Presence of binaries never enables this profile.
// Requests cannot supply paths, engine versions, digests or renderer packages.
type MooringCommandHTTPConfig struct {
	Enabled                    bool
	HostBinary, HostSHA256     string
	EngineBinary, EngineSHA256 string
	EditorBundle               func(*http.Request) (digest, indexURL string)
}

func (m *collaborationHTTPManager) commands(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.commandFactory == nil {
		replyError(w, 503, "native command editing is disabled; document data retained")
		return
	}
	m.commandFactory.serve(w, r)
}
