package restricteddispatch

import "github.com/crewship-ai/crewship/internal/access"

// DelegatedRunRequest is constructed only by the host's immutable workflow
// declaration. Public handlers never decode it from model or task JSON.
type DelegatedRunRequest struct {
	User, Workspace, Agent, Chat, ParentHandle, Input string
	Rights                                            []access.Right
	SourceEntryIDs                                    []string
}
