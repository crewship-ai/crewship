package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Approval continuations are single-use, bounded, session-bound and tied to
// the entire canonical request. A model cannot attach a fabricated response
// to a first call, change the body after approval, or replay a consumed token.
// The client is still trusted to present the form to a human.
type mcpPendingApproval struct {
	session *mcp.ServerSession
	digest  [32]byte
	expires time.Time
}
type mcpApprovalGate struct {
	mu      sync.Mutex
	pending map[string]mcpPendingApproval
}

// approvalSnapshot pins the credentials checked by the approval gate through
// execution. A login change while the form is pending invalidates its receipt.
func (s *cliMCP) approvalSnapshot() (*cliMCP, [32]byte, error) {
	client := s.client
	if s.refreshClient != nil {
		var err error
		client, err = s.refreshClient()
		if err != nil {
			return nil, [32]byte{}, err
		}
	} else if s.authenticate != nil {
		if err := s.authenticate(); err != nil {
			return nil, [32]byte{}, err
		}
	}
	snapshot := *s
	clientCopy := *client
	snapshot.client = &clientCopy
	snapshot.refreshClient = nil
	snapshot.authenticate = func() error { return nil }
	raw, _ := json.Marshal([]string{clientCopy.BaseURL, clientCopy.WorkspaceID, clientCopy.Token})
	return &snapshot, sha256.Sum256(raw), nil
}

func (g *mcpApprovalGate) approve(req *mcp.CallToolRequest, in mcpRequestInput, credentialRevision [32]byte) (*mcp.CallToolResult, error) {
	capabilities := req.Session.InitializeParams().Capabilities
	if capabilities == nil || capabilities.Elicitation == nil || (capabilities.Elicitation.Form == nil && capabilities.Elicitation.URL != nil) {
		return nil, apiValidation("human approval unavailable; operation was not executed")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(append(raw, credentialRevision[:]...))
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == nil {
		g.pending = map[string]mcpPendingApproval{}
	}
	now := time.Now()
	for token, approval := range g.pending {
		if !now.Before(approval.expires) {
			delete(g.pending, token)
		}
	}
	if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
		previous, ok := g.pending[req.Params.RequestState]
		delete(g.pending, req.Params.RequestState)
		if !ok || previous.session != req.Session || previous.digest != digest {
			return nil, apiValidation("approval continuation is missing, expired, changed or already used")
		}
		response, ok := req.Params.InputResponses["approve"].(*mcp.ElicitResult)
		if !ok || response.Action != "accept" || response.Content["approve"] != true {
			return nil, apiValidation("human approval declined; operation was not executed")
		}
		return nil, nil
	}
	if len(g.pending) >= 128 {
		return nil, apiValidation("too many pending approvals; wait for expiry or restart the MCP connection")
	}
	token := rand.Text()
	g.pending[token] = mcpPendingApproval{req.Session, digest, now.Add(5 * time.Minute)}
	// SDK bridges this response to legacy elicitation/create for older clients.
	// Current clients fulfill InputRequests and retry with the opaque state.
	return &mcp.CallToolResult{RequestState: token, InputRequests: mcp.InputRequestMap{"approve": &mcp.ElicitParams{
		Mode: "form", Message: "Approve Crewship operation " + in.OperationID + " on the configured server/workspace? Inspect the pending tool arguments before approving.",
		RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"approve": map[string]any{"type": "boolean", "title": "Approve this operation"}}, "required": []string{"approve"}},
	}}}, nil
}
