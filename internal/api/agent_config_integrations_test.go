package api

import (
	"strings"
	"testing"
)

// TestBuildConnectedIntegrationsBlock verifies the [CONNECTED INTEGRATIONS]
// system-prompt block: empty when no servers resolve, and an explicit
// "you already have access — use the tools" directive listing each connected
// integration when they do. This is what stops an agent from reflexively
// answering "I have no access to your YouTube account" when YouTube tools are
// in fact wired in via Composio. (See screenshots in the feat that motivated it.)
func TestBuildConnectedIntegrationsBlock(t *testing.T) {
	// No servers → no block.
	if got := buildConnectedIntegrationsBlock(nil); got != "" {
		t.Errorf("expected empty block for nil servers, got %q", got)
	}
	if got := buildConnectedIntegrationsBlock([]mcpServerEntry{}); got != "" {
		t.Errorf("expected empty block for empty servers, got %q", got)
	}

	// A single Composio YouTube binding (Full access) — the resolver renders
	// display names like "Composio: youtube · Full".
	yt := mcpServerEntry{
		ID:          "composio-ws-youtube",
		Name:        "composio-ws-youtube",
		DisplayName: "Composio: youtube · Full",
	}
	block := buildConnectedIntegrationsBlock([]mcpServerEntry{yt})
	for _, want := range []string{
		"[CONNECTED INTEGRATIONS]",
		"Composio: youtube · Full",
		"[END CONNECTED INTEGRATIONS]",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("integrations block missing %q\n---\n%s", want, block)
		}
	}
	// Must carry the behavioural directive so the agent stops claiming no access.
	low := strings.ToLower(block)
	if !strings.Contains(low, "do not") && !strings.Contains(low, "don't") {
		t.Errorf("integrations block missing a do-not-claim-no-access directive:\n%s", block)
	}
	if !strings.Contains(low, "tool") {
		t.Errorf("integrations block should reference the agent's tools:\n%s", block)
	}

	// Empty DisplayName falls back to Name; multiple servers all listed.
	multi := buildConnectedIntegrationsBlock([]mcpServerEntry{
		{Name: "github-mcp", DisplayName: ""},
		{Name: "composio-ws-gmail", DisplayName: "Composio: gmail · Read"},
	})
	for _, want := range []string{"github-mcp", "Composio: gmail · Read"} {
		if !strings.Contains(multi, want) {
			t.Errorf("multi block missing %q\n---\n%s", want, multi)
		}
	}

	// A server with neither name nor display name contributes nothing and must
	// not produce a dangling empty bullet.
	if strings.Contains(buildConnectedIntegrationsBlock([]mcpServerEntry{{}}), "- \n") {
		t.Error("block should not contain an empty bullet for an unlabeled server")
	}
}

// TestBuildConnectedIntegrationsBlock_ToolNames verifies #862 slice 3: when a
// server carries enabled tool names they render inline ("Gmail: TOOL, …") so
// the agent knows WHAT it can do, and a server with no tools still lists its
// label alone.
func TestBuildConnectedIntegrationsBlock_ToolNames(t *testing.T) {
	block := buildConnectedIntegrationsBlock([]mcpServerEntry{
		{Name: "gmail", DisplayName: "Gmail", Tools: []string{"GMAIL_FETCH_EMAIL", "GMAIL_SEND"}},
		{Name: "github", DisplayName: "GitHub"}, // no tools → label only
	})
	if !strings.Contains(block, "Gmail: GMAIL_FETCH_EMAIL, GMAIL_SEND") {
		t.Errorf("tool names not rendered inline:\n%s", block)
	}
	// The tool-less integration still appears, as a bare label (no trailing ": ").
	if !strings.Contains(block, "- GitHub\n") {
		t.Errorf("tool-less integration should list its bare label:\n%s", block)
	}
}
