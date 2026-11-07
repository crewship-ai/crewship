// Writes <outputDir>/finished.json when Playwright ends the run, with its own
// overall status and the runner's PRD_RUN_ID. verdict.mjs refuses a run without
// it, or with a marker of another run: an interrupted or crashed run leaves none,
// however green its rows look. Only a real run loads this reporter; `--list` does
// not (playwright.native-walk.config.ts).
import * as fs from "node:fs"
import * as path from "node:path"
import type { FullResult, Reporter } from "@playwright/test/reporter"
import { readPrdEnv } from "../prd/env"

export default class NativeWalkFinishReporter implements Reporter {
  private outputDir = readPrdEnv().outputDir
  private runId = process.env.PRD_RUN_ID?.trim() || null

  onEnd(result: FullResult) {
    if (!this.runId) return
    fs.mkdirSync(this.outputDir, { recursive: true })
    const marker = { runId: this.runId, status: result.status, startedAt: result.startTime.toISOString(), durationMs: result.duration, finishedAt: new Date().toISOString() }
    fs.writeFileSync(path.join(this.outputDir, "finished.json"), JSON.stringify(marker, null, 2) + "\n")
  }

  printsToStdio() {
    return false
  }
}
