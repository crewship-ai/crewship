package api

import "testing"

// ---------------------------------------------------------------------------
// resolveAgentFilePath — what an agent's Files panel may open.
//
// The regression this pins: handleFileList, given an agent_slug, merges the
// CREW-ROOT files into that agent's listing on purpose ("files the agent
// saved to /output/ instead of /output/<agent-slug>/"). The download then
// refused those exact paths with 403 "path not scoped to this agent". List
// and download disagreed about what belongs to an agent, and it was the
// common case rather than a corner — an agent told to write a report puts it
// in the working directory, which is the crew root.
//
// The sibling-namespace refusal is the part that must NOT relax.
// ---------------------------------------------------------------------------

func TestResolveAgentFilePath(t *testing.T) {
	const crew = "crew123"
	const slug = "riley"

	tests := []struct {
		name    string
		in      string
		want    string
		allowed bool
	}{
		{
			name:    "relative path gets this agent's prefix",
			in:      "workspace/config.toml",
			want:    "crew123/riley/workspace/config.toml",
			allowed: true,
		},
		{
			name:    "this agent's own full path passes through",
			in:      "crew123/riley/workspace/config.toml",
			want:    "crew123/riley/workspace/config.toml",
			allowed: true,
		},
		{
			name:    "a crew-root file is allowed — the listing hands it out",
			in:      "crew123/report.md",
			want:    "crew123/report.md",
			allowed: true,
		},
		{
			name:    "a crew-root dotfile is still a crew-root file",
			in:      "crew123/.notes",
			want:    "crew123/.notes",
			allowed: true,
		},
		{
			name:    "a sibling agent's namespace is refused",
			in:      "crew123/morgan/secrets.md",
			allowed: false,
		},
		{
			name:    "a sibling's nested file is refused",
			in:      "crew123/morgan/deep/nested/file.md",
			allowed: false,
		},
		{
			name:    "the crew root itself is not a file",
			in:      "crew123/",
			allowed: false,
		},
		{
			name: "an agent whose slug prefixes another's does not reach it",
			// "riley2" starts with "riley" as a STRING but is a different
			// agent. The separator in the prefix is what keeps them apart, and
			// this asserts the prefix keeps carrying it.
			in:      "crew123/riley2/notes.md",
			allowed: false,
		},
		{
			name:    "another crew is unreachable — it reads as a relative path",
			in:      "othercrew/riley/x.md",
			want:    "crew123/riley/othercrew/riley/x.md",
			allowed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, allowed := resolveAgentFilePath(crew, slug, tc.in)
			if allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v (got path %q)", allowed, tc.allowed, got)
			}
			if allowed && got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsProtectedAgentConfigPath(t *testing.T) {
	const (
		crew = "crew123"
		slug = "riley"
	)

	protected := []string{
		".mcp.json",
		".cursor/mcp.json",
		".factory/mcp.json",
		".gemini/settings.json",
		"opencode.json",
		".codex/config.toml",
	}
	for _, relative := range protected {
		for _, path := range []string{relative, crew + "/" + slug + "/" + relative} {
			t.Run(path, func(t *testing.T) {
				if !isProtectedAgentConfigPath(crew, slug, path) {
					t.Fatalf("isProtectedAgentConfigPath(%q) = false, want true", path)
				}
			})
		}
	}

	allowed := []string{
		"docs/.mcp.json",
		".codex/skills/reviewer/SKILL.md",
		".gemini/skills/reviewer/SKILL.md",
		".factory/notes.md",
		crew + "/report.md",
		crew + "/" + slug + "/docs/opencode.json",
		crew + "/other-agent/.mcp.json",
	}
	for _, path := range allowed {
		t.Run("allowed/"+path, func(t *testing.T) {
			if isProtectedAgentConfigPath(crew, slug, path) {
				t.Fatalf("isProtectedAgentConfigPath(%q) = true, want false", path)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsProtectedCrewConfigPath — the crew door's generalization of
// isProtectedAgentConfigPath (#2142).
//
// The agent door knows exactly one slug, so it can require an exact
// "<crewID>/<slug>/" prefix. The crew door has no such context — a request
// can legitimately name any agent in the crew — so this must deny the six
// generated files under WHICHEVER slug the path names, not just one. The
// case that matters most here is "other-agent's config, reached through the
// crew door": isProtectedAgentConfigPath(crew, slug, ...) says false for it
// (correctly — it isn't THIS agent's file), but the crew-door bypass
// (#2142) was exactly a caller reaching another agent's config where no
// per-agent check ran at all.
// ---------------------------------------------------------------------------

func TestIsProtectedCrewConfigPath(t *testing.T) {
	const crew = "crew123"

	protected := []string{
		".mcp.json",
		".cursor/mcp.json",
		".factory/mcp.json",
		".gemini/settings.json",
		"opencode.json",
		".codex/config.toml",
	}
	for _, relative := range protected {
		for _, agentSlug := range []string{"riley", "other-agent", "morgan"} {
			path := crew + "/" + agentSlug + "/" + relative
			t.Run(path, func(t *testing.T) {
				if !IsProtectedCrewConfigPath(crew, path) {
					t.Fatalf("IsProtectedCrewConfigPath(%q, %q) = false, want true", crew, path)
				}
			})
		}
	}

	allowed := []string{
		"crew123/riley/docs/.mcp.json",     // nested, not the file itself
		"crew123/riley/docs/opencode.json", // nested
		"crew123/report.md",                // crew-root file, no agent segment
		"crew123/.notes",                   // crew-root dotfile
		"crew123",                          // the crew root itself
		"crew123/",                         // trailing slash, no file
		"othercrew/riley/.mcp.json",        // a different crew id entirely
		"crew123/riley/.codex/skills/x/SKILL.md",
	}
	for _, path := range allowed {
		t.Run("allowed/"+path, func(t *testing.T) {
			if IsProtectedCrewConfigPath(crew, path) {
				t.Fatalf("IsProtectedCrewConfigPath(%q, %q) = true, want false", crew, path)
			}
		})
	}
}
