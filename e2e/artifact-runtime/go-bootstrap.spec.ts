import { test, expect } from '@playwright/test'
import { mkdtemp, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { startGoArtifactBootstrap, parseGoRuntimePort } from './go-bootstrap'

test('Go runtime readiness accepts only one canonical bounded decimal port line', () => {
  expect(parseGoRuntimePort('CREWSHIP_RUNTIME_READY_PORT=1')).toBe(1)
  expect(parseGoRuntimePort('CREWSHIP_RUNTIME_READY_PORT=65535')).toBe(65535)
  for (const line of ['', 'CREWSHIP_RUNTIME_READY_PORT=', 'CREWSHIP_RUNTIME_READY_PORT=0', 'CREWSHIP_RUNTIME_READY_PORT=65536', 'CREWSHIP_RUNTIME_READY_PORT=-1', 'CREWSHIP_RUNTIME_READY_PORT=01', 'CREWSHIP_RUNTIME_READY_PORT=123\r', 'CREWSHIP_RUNTIME_READY_PORT=123\n', 'CREWSHIP_RUNTIME_READY_PORT=123/path', 'CREWSHIP_RUNTIME_READY_PORT=123?data=private', 'CREWSHIP_RUNTIME_READY_PORT=http://attacker.test', ' CREWSHIP_RUNTIME_READY_PORT=123', 'CREWSHIP_RUNTIME_READY_PORT=123\nother']) expect(parseGoRuntimePort(line)).toBeNull()
})


test('Go bootstrap rejects missing or invalid readiness and cleans up its owned child', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'artifact-invalid-readiness-'))
  const previous = process.env.CREWSHIP_TEST_RUNTIME_BINARY
  try {
    for (const [name, output] of [['missing', ''], ['invalid', 'CREWSHIP_RUNTIME_READY_PORT=65536\n']]) {
      const binary = join(directory, name)
      await writeFile(binary, `#!/usr/bin/env node\nprocess.stdout.write(${JSON.stringify(output)});setInterval(()=>{},1000)\n`, { mode: 0o700 })
      process.env.CREWSHIP_TEST_RUNTIME_BINARY = binary
      await expect(startGoArtifactBootstrap()).rejects.toThrow(name === 'missing' ? 'did not announce its readiness port' : 'invalid or duplicate readiness port')
    }
  } finally {
    if (previous === undefined) delete process.env.CREWSHIP_TEST_RUNTIME_BINARY
    else process.env.CREWSHIP_TEST_RUNTIME_BINARY = previous
    await rm(directory, { recursive: true, force: true })
  }
})
