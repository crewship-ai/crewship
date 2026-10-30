// Writes one JSON row per (test, PRD requirement) into <outputDir>/rows and a
// summary table at the end. Rows come from the reporter, not from the test
// body, so failed, timed-out and skipped tests are recorded too.
import * as fs from "node:fs"
import * as path from "node:path"
import type { FullResult, Reporter, TestCase, TestResult } from "@playwright/test/reporter"
import { outputPaths, readPrdEnv } from "./env"
import { PRD_EVIDENCE, PRD_REQUIREMENT, parseRequirement } from "./requirements"
import { resultOf, readRows, slug, writeSummary } from "./report.mjs"
import type { RunInfo } from "./global-setup"

export default class PrdAcceptanceReporter implements Reporter {
  private env = readPrdEnv()
  private paths = outputPaths(this.env.outputDir)

  onBegin() {
    fs.mkdirSync(this.paths.rows, { recursive: true })
    for (const f of fs.readdirSync(this.paths.rows)) {
      if (f.endsWith(".json")) fs.rmSync(path.join(this.paths.rows, f))
    }
  }

  private run(): RunInfo | null {
    try {
      return JSON.parse(fs.readFileSync(this.paths.run, "utf8")) as RunInfo
    } catch {
      return null
    }
  }

  onTestEnd(test: TestCase, result: TestResult) {
    const annotations = [...test.annotations, ...result.annotations].filter((a) => a.type !== PRD_EVIDENCE)
    const seen = new Set<string>()
    const requirements = annotations
      .filter((a) => a.type === PRD_REQUIREMENT && a.description)
      .map((a) => parseRequirement(a.description as string))
      .filter((r) => !seen.has(r.id) && seen.add(r.id))
    const scenario = path.basename(test.location.file).replace(/\.acceptance\.ts$/, "")
    const evidenceFiles = [...new Set(result.annotations.filter((a) => a.type === PRD_EVIDENCE && a.description).map((a) => a.description as string))]
    const skip = annotations.find((a) => a.type === "skip")?.description ?? null
    // eslint-disable-next-line no-control-regex
    const error = result.error?.message?.replace(/\u001b\[[0-9;]*m/g, "").split("\n")[0]?.slice(0, 300) ?? null
    let status = resultOf(result.status)
    let reason = status === "skipped" ? skip : status === "failed" ? error : null
    if (requirements.length === 0) {
      requirements.push({ id: `UNMAPPED ${test.title}`, title: null })
      if (status !== "skipped") {
        status = "failed"
        reason = "test declares no PRD requirement (use prdRequirement())"
      }
    }
    const run = this.run()
    for (const req of requirements) {
      const row = {
        requirement: req.id,
        title: req.title,
        scenario,
        test: test.title,
        result: status,
        appSha: run?.appSha ?? "unknown",
        evidence: evidenceFiles[0] ?? null,
        evidenceFiles,
        mode: this.env.mode,
        origin: this.env.origin,
        reason,
        durationMs: result.duration,
        finishedAt: new Date().toISOString(),
      }
      // Same file name for every retry attempt: the final attempt wins.
      const file = `${slug(scenario)}--${slug(req.id)}--${slug(test.id)}.json`
      fs.writeFileSync(path.join(this.paths.rows, file), JSON.stringify(row, null, 2) + "\n")
    }
  }

  onEnd(_result: FullResult) {
    writeSummary(this.env.outputDir, readRows(this.paths.rows), this.run())
  }

  printsToStdio() {
    return false
  }
}
