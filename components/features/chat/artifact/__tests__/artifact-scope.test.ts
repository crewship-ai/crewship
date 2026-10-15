import { describe, expect, it } from "vitest"
import { isClientArtifactPath } from "../artifact-scope"

describe("client-facing artifact paths", () => {
  it("shows output formats that have an artifact viewer", () => {
    for (const path of ["report.pdf", "exports/plan.csv", "site/mockup.html", "images/hero.png", "budget.xlsx", "runs/msg_1/report.pdf", "runs/run-1/exports/plan.csv", "runs/run_2/budget.xlsx", "runs/run_3/budget.xlsm"]) {
      expect(isClientArtifactPath(path)).toBe(true)
    }
  })

  it("keeps agent settings, run scaffolding and ordinary Markdown out of Chat", () => {
    for (const path of ["AGENTS.md", "brief.md", "runs/msg_1/AGENTS.md", ".cursor/snapshot.pdf", "attachments/chat_1/report.pdf", "settings/CLAUDE.md",
      "runs/report.pdf", "runs//report.pdf", "runs/../report.pdf", "runs/.hidden/report.pdf", "runs/attachments/report.pdf", `runs/${"x".repeat(81)}/report.pdf`,
      "runs/msg_1/.codex/report.pdf", "runs/msg_1/attachments/chat_1/report.pdf", "runs/msg_1/runs/run_2/report.pdf", "crew/runs/agent/run_1/report.pdf", "exports/runs/run_1/report.pdf", "runs/run_3/.hidden/budget.xlsm", "attachments/budget.xlsm", "budget.xlsb", "budget.ods"]) {
      expect(isClientArtifactPath(path)).toBe(false)
    }
  })
})
