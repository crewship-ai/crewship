package groupchat

import "net/url"

// Use the same stored avatar and inherited crew style as agent Chat. Resolve at
// read time so changing a profile updates history without rewriting messages.
const agentIdentityCols = `json_object('id',a.id,'workspace_id',a.workspace_id,'slug',a.slug,'seed',COALESCE(NULLIF(a.avatar_seed,''),a.slug),'style',COALESCE(a.avatar_style,cr.avatar_style,''),'hash',COALESCE(a.avatar_svg_hash,''))`

type agentIdentity struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Slug        string `json:"slug"`
	Seed        string `json:"seed"`
	Style       string `json:"style"`
	Hash        string `json:"hash"`
}

func (a agentIdentity) url() string {
	if a.ID == "" || a.WorkspaceID == "" || a.Hash == "" {
		return ""
	}
	return "/api/v1/agents/" + url.PathEscape(a.ID) + "/avatar?v=" + url.QueryEscape(a.Hash) + "&workspace_id=" + url.QueryEscape(a.WorkspaceID)
}
