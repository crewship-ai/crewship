package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// cli_adapter.go — writeFileViaContainer permission bits.
//
// The regression these pin:
//
//   writeFileViaContainer applied `chmod 600` to EVERY file it wrote, and the
//   files it writes are two different kinds of thing. MCP configs can hold a
//   literal API token, and 600 is right for those. The canonical memory files
//   and skill bodies hold the system prompt — what the operator wrote, that
//   the product offers to show back — and at 600 owned by UID 1001 they were
//   unreadable by crewshipd, which runs as the host user. `List` succeeded on
//   the 0755 directory while `Read` took EACCES on every entry, so the Files
//   panel listed a full tree and answered "file not found" for all of it.
//
// A mode is asserted per KIND rather than per path so adding a sixth memory
// file or a seventh skill root cannot quietly ship at the wrong permissions.
// ---------------------------------------------------------------------------

// chmodModes returns the mode argument of every `chmod` in the scripts the
// fake container saw, in order.
func chmodModes(scripts []string) []string {
	var out []string
	for _, s := range scripts {
		idx := strings.Index(s, "chmod ")
		if idx < 0 {
			continue
		}
		rest := s[idx+len("chmod "):]
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			out = append(out, rest[:sp])
		}
	}
	return out
}

func TestWriteFileViaContainer_ModeIsPerKind(t *testing.T) {
	tests := []struct {
		name     string
		write    func(t *testing.T, fake *adapterTestContainer)
		wantMode string
		// The minimum number of chmod'd writes the call must produce, so a
		// silently-skipped write cannot pass by asserting over an empty list.
		wantAtLeast int
	}{
		{
			name: "canonical memory files are readable",
			write: func(t *testing.T, fake *adapterTestContainer) {
				t.Helper()
				req := AgentRunRequest{SystemPrompt: "Be helpful."}
				if err := writeCanonicalMemoryFiles(
					context.Background(), fake, "ct-1", req, "/work", quietAdapterLogger(),
				); err != nil {
					t.Fatalf("writeCanonicalMemoryFiles: %v", err)
				}
			},
			wantMode:    "644",
			wantAtLeast: 5, // AGENTS/CLAUDE/GEMINI + .cursor/rules + .factory
		},
		{
			name: "skill bodies are readable",
			write: func(t *testing.T, fake *adapterTestContainer) {
				t.Helper()
				skills := []SkillBundle{{Slug: "network-probe", Content: "# Probe"}}
				if err := writeAgentSkills(
					context.Background(), fake, "ct-1", "/work", skills, quietAdapterLogger(),
				); err != nil {
					t.Fatalf("writeAgentSkills: %v", err)
				}
			},
			wantMode:    "644",
			wantAtLeast: 1,
		},
		{
			name: "cursor MCP config stays secret",
			write: func(t *testing.T, fake *adapterTestContainer) {
				t.Helper()
				req := AgentRunRequest{AgentMCPConfigJSON: `{"mcpServers":{"x":{"command":"y"}}}`}
				if err := writeMCPCursor(
					context.Background(), fake, "ct-1", req, "/work", quietAdapterLogger(),
				); err != nil {
					t.Fatalf("writeMCPCursor: %v", err)
				}
			},
			wantMode:    "600",
			wantAtLeast: 1,
		},
		{
			name: "droid MCP config stays secret",
			write: func(t *testing.T, fake *adapterTestContainer) {
				t.Helper()
				req := AgentRunRequest{AgentMCPConfigJSON: `{"mcpServers":{"x":{"command":"y"}}}`}
				if err := writeMCPDroid(
					context.Background(), fake, "ct-1", req, "/work", quietAdapterLogger(),
				); err != nil {
					t.Fatalf("writeMCPDroid: %v", err)
				}
			},
			wantMode:    "600",
			wantAtLeast: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &adapterTestContainer{}
			tc.write(t, fake)

			modes := chmodModes(fake.execScripts)
			if len(modes) < tc.wantAtLeast {
				t.Fatalf("chmod'd writes = %d (%v), want ≥ %d", len(modes), modes, tc.wantAtLeast)
			}
			for i, got := range modes {
				if got != tc.wantMode {
					t.Errorf("write %d: chmod %s, want chmod %s", i, got, tc.wantMode)
				}
			}
		})
	}
}

// A secret and a readable file must never resolve to the same bits — the
// whole point of splitting them. Cheap, and it fails loudly if someone
// "simplifies" the two constants back into one.
func TestContainerFileModes_AreDistinct(t *testing.T) {
	if containerFileSecret == containerFileReadable {
		t.Fatal("secret and readable modes are the same value")
	}
	if containerFileSecret != "600" {
		t.Errorf("containerFileSecret = %q, want 600", containerFileSecret)
	}
	if containerFileReadable != "644" {
		t.Errorf("containerFileReadable = %q, want 644", containerFileReadable)
	}
}
