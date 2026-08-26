import { describe, it, expect } from "vitest"

import { classifyAgentFile, relativeToAgent } from "../files/file-scope"

describe("classifyAgentFile", () => {
  it("calls the agent's own artefacts created", () => {
    expect(classifyAgentFile("report.md")).toBe("created")
    expect(classifyAgentFile("docs/runbook.md")).toBe("created")
    expect(classifyAgentFile("src/main.go")).toBe("created")
  })

  it("calls chat attachments shared", () => {
    expect(classifyAgentFile("attachments/chat-1/photo.png")).toBe("shared")
  })

  it("calls Crewship's own scaffolding plumbing", () => {
    // The five copies of the system prompt, and the skill bodies under every
    // discovery root a CLI might look in.
    for (const p of [
      "AGENTS.md",
      "CLAUDE.md",
      "GEMINI.md",
      "opencode.json",
      ".mcp.json",
      ".claude/skills/network-probe/SKILL.md",
      ".agents/skills/routine-author/SKILL.md",
      ".cursor/rules/crewship.md",
      ".factory/AGENTS.md",
      ".opencode/skills/x/SKILL.md",
      ".codex/config.toml",
      ".gemini/settings.json",
    ]) {
      expect(classifyAgentFile(p), p).toBe("plumbing")
    }
  })

  it("does not hide an agent's own file just because of its name", () => {
    // Hiding this would be the panel lying about the agent's work: a
    // CLAUDE.md the agent WROTE, in a directory it chose, is an artefact.
    expect(classifyAgentFile("docs/CLAUDE.md")).toBe("created")
    expect(classifyAgentFile("project/AGENTS.md")).toBe("created")
  })

  it("tolerates a leading slash and an empty path", () => {
    expect(classifyAgentFile("/AGENTS.md")).toBe("plumbing")
    expect(classifyAgentFile("")).toBe("created")
  })

  it("does not treat a prefix collision as a match", () => {
    // ".claudex" is not ".claude".
    expect(classifyAgentFile(".claudex/notes.md")).toBe("created")
    expect(classifyAgentFile("AGENTS.md.bak")).toBe("created")
  })
})

describe("relativeToAgent", () => {
  it("strips the agent's own prefix", () => {
    expect(relativeToAgent("crew1/riley/report.md", "crew1", "riley")).toBe("report.md")
  })

  it("strips the crew prefix for a crew-root file", () => {
    // This is where an agent told to "write report.md" actually puts it —
    // the working directory IS the crew root.
    expect(relativeToAgent("crew1/report.md", "crew1", "riley")).toBe("report.md")
  })

  it("leaves an already-relative path alone", () => {
    expect(relativeToAgent("report.md", "crew1", "riley")).toBe("report.md")
  })

  it("survives a missing crew or slug", () => {
    expect(relativeToAgent("crew1/riley/report.md", null, null)).toBe("crew1/riley/report.md")
    expect(relativeToAgent("crew1/report.md", "crew1", null)).toBe("report.md")
  })
})
