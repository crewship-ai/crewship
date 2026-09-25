import { describe, expect, it } from "vitest"
import { isClientArtifactPath } from "../artifact-scope"

describe("client-facing artifact paths", () => {
  it("shows output formats that have an artifact viewer", () => {
    for (const path of ["report.pdf", "exports/plan.csv", "site/mockup.html", "images/hero.png", "budget.xlsx"]) {
      expect(isClientArtifactPath(path)).toBe(true)
    }
  })

  it("keeps agent settings, run scaffolding and ordinary Markdown out of Chat", () => {
    for (const path of ["AGENTS.md", "brief.md", "runs/msg_1/AGENTS.md", "runs/msg_1/report.pdf", ".cursor/snapshot.pdf", "attachments/chat_1/report.pdf", "settings/CLAUDE.md"]) {
      expect(isClientArtifactPath(path)).toBe(false)
    }
  })
})
