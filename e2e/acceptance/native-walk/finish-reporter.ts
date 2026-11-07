// Writes <outputDir>/finished.json when Playwright ends the run, with its own
// overall status. verdict.mjs refuses a run without it: an interrupted or crashed
// run leaves no marker, however green its rows look.
import * as fs from "node:fs"
import * as path from "node:path"
import type { FullResult, Reporter } from "@playwright/test/reporter"
import { readPrdEnv } from "../prd/env"

export default class NativeWalkFinishReporter implements Reporter {
  private outputDir = readPrdEnv().outputDir

  onEnd(result: FullResult) {
    fs.mkdirSync(this.outputDir, { recursive: true })
    const marker = { status: result.status, startedAt: result.startTime.toISOString(), durationMs: result.duration, finishedAt: new Date().toISOString() }
    fs.writeFileSync(path.join(this.outputDir, "finished.json"), JSON.stringify(marker, null, 2) + "\n")
  }

  printsToStdio() {
    return false
  }
}
