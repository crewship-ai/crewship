// The shared PRD reporter, wrapped so that its completion is proven. Playwright logs and
// swallows an exception from a reporter callback, so finished.json alone can say "passed"
// after the rows or the summary were never written. This wrapper writes
// <outputDir>/reporter.json (run id and row count) only when every callback of the PRD
// reporter returned and the summary exists; verdict.mjs refuses a run without it.
import * as fs from "node:fs"
import * as path from "node:path"
import type { FullResult, Reporter, TestCase, TestResult } from "@playwright/test/reporter"
import { outputPaths, readPrdEnv } from "../prd/env"
import PrdAcceptanceReporter from "../prd/reporter"
import { readRows } from "../prd/report.mjs"

export default class NativeWalkRowsReporter implements Reporter {
  private inner = new PrdAcceptanceReporter()
  private outputDir = readPrdEnv().outputDir
  private runId = process.env.PRD_RUN_ID?.trim() || null
  private errors: string[] = []

  private guard(what: string, fn: () => void) {
    try {
      fn()
    } catch (err) {
      this.errors.push(`${what}: ${String(err).slice(0, 300)}`)
      console.error(`[native-walk] PRD reporter ${what} failed: ${String(err).slice(0, 300)}`)
    }
  }

  onBegin() {
    this.guard("onBegin", () => this.inner.onBegin())
  }

  onTestEnd(test: TestCase, result: TestResult) {
    this.guard(`onTestEnd ${test.title}`, () => this.inner.onTestEnd(test, result))
  }

  onEnd(result: FullResult) {
    this.guard("onEnd", () => this.inner.onEnd(result))
    if (!this.runId || this.errors.length) return
    const paths = outputPaths(this.outputDir)
    if (!fs.existsSync(path.join(this.outputDir, "summary.json"))) return
    const rows = readRows(paths.rows).length
    fs.writeFileSync(path.join(this.outputDir, "reporter.json"), JSON.stringify({ runId: this.runId, rows, finishedAt: new Date().toISOString() }, null, 2) + "\n")
  }

  printsToStdio() {
    return false
  }
}
