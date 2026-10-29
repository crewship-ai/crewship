// @vitest-environment node
import { execFileSync } from 'node:child_process'
import { it } from 'vitest'

it('rejects incomplete, stale, widened and modified-runtime engine evidence', () => {
  execFileSync('python3', ['-m', 'unittest', 'discover', '-s', 'tools/wordcraft-capabilities/c1-qualification', '-p', '*_test.py'], { stdio: 'pipe' })
})
