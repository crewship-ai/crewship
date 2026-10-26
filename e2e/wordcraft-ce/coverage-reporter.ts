import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { FullResult, Reporter, TestCase, TestResult } from '@playwright/test/reporter'
import { STAGES } from './contract'
import { features } from './features'

export interface CoverageRow { feature: string; group: string; result: string; stages: Record<string, string> }
export function completeCoverage(rows: CoverageRow[]): CoverageRow[] {
  return features.map(feature => rows.find(row => row.feature === feature.id) ?? { feature: feature.id, group: feature.group, result: 'not-executed', stages: Object.fromEntries(STAGES.map(stage => [stage, 'not-reached'])) })
}
export function summarizeCoverage(rows: CoverageRow[]) {
  const groups: Record<string, Record<string, { passed: number; total: number }>> = {}
  for (const row of rows) {
    groups[row.group] ??= Object.fromEntries(STAGES.map(stage => [stage, { passed: 0, total: 0 }]))
    for (const stage of STAGES) {
      groups[row.group][stage].total++
      if (row.stages[stage] === 'passed') groups[row.group][stage].passed++
    }
  }
  return groups
}
export default class WordCraftCoverageReporter implements Reporter {
  private rows: CoverageRow[] = []
  onTestEnd(test: TestCase, result: TestResult) {
    const coverage = result.attachments.find(item => item.name === 'wordcraft-stage-coverage')
    const details = coverage?.body ? JSON.parse(coverage.body.toString()) as { feature: string; group: string; stages: Record<string, string> } : undefined
    if (!test.annotations.some(item => item.type === 'wordcraft-feature')) return
    this.rows.push({ feature: details?.feature ?? test.annotations.find(item => item.type === 'wordcraft-feature')?.description ?? test.title, group: details?.group ?? test.annotations.find(item => item.type === 'wordcraft-group')?.description ?? 'unknown', result: result.status,
      stages: Object.fromEntries(STAGES.map(stage => [stage, details?.stages[stage] ?? 'not-reached'])) })
  }
  onEnd(result: FullResult) {
    const directory = resolve(process.env.WORDCRAFT_CE_OUTPUT_DIR ?? 'test-results/wordcraft-ce')
    mkdirSync(directory, { recursive: true })
    this.rows = completeCoverage(this.rows)
    const executed = this.rows.filter(row => row.result !== 'not-executed').length
    const report = { expected: features.length, executed, coverageComplete: executed === features.length, schemaVersion: 1, result: result.status, evidence: 'browser acceptance execution only; no deployment claim', rows: this.rows, groups: summarizeCoverage(this.rows) }
    writeFileSync(resolve(directory, 'coverage.json'), JSON.stringify(report, null, 2) + '\n')
    const names = ['Funkce', 'Skupina', ...STAGES]
    const table = [names, names.map(() => '---'), ...this.rows.map(row => [row.feature, row.group, ...STAGES.map(stage => row.stages[stage])])]
    writeFileSync(resolve(directory, 'coverage.md'), table.map(row => `| ${row.join(' | ')} |`).join('\n') + '\n')
  }
}
