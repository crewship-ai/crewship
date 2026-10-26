// @vitest-environment node
import { expect, it } from 'vitest'
import { completeCoverage, summarizeCoverage, type CoverageRow } from './coverage-reporter'
import { features } from './features'
import { STAGES } from './contract'

it('keeps missing or filtered features in every stage denominator', () => {
  const one: CoverageRow = { feature: 'text', group: 'text-enter', result: 'passed', stages: Object.fromEntries(STAGES.map(stage => [stage, 'passed'])) }
  const rows = completeCoverage([one])
  expect(rows).toHaveLength(features.length)
  expect(rows.find(row => row.feature === 'enter')).toMatchObject({ result: 'not-executed', stages: { undo: 'not-reached' } })
  expect(summarizeCoverage(rows)['text-enter'].saved).toEqual({ passed: 1, total: 2 })
  expect(completeCoverage([]).every(row => row.result === 'not-executed')).toBe(true)
})
