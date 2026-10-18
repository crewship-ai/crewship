package access

// PublicationProviderSnapshot contains only immutable nonsecret declaration
// identities captured by the host in the caller-owned publication transaction.
// It grants neither provider access nor execution admission.
type PublicationProviderSnapshot struct {
	WorkspaceID        string `json:"-"`
	CrewID             string `json:"-"`
	AgentID            string `json:"-"`
	Profile            string `json:"-"`
	PolicyHash         string `json:"-"`
	CredentialID       string `json:"-"`
	GrantID            string `json:"-"`
	CredentialRevision string `json:"-"`
	Model              string `json:"-"`
	GrantExpiry        string `json:"-"`
	KeyExpiry          string `json:"-"`
}
