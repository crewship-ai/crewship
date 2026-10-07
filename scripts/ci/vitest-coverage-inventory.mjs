// Discover the uncovered-source baseline using the locked Vitest provider's
// own configuration/glob/exclusion semantics. This does not run tests or clean
// coverage output. The provider API is internal: upgrades fail closed if it
// changes, instead of silently approximating its glob rules in Python.
import { createVitest } from 'vitest/node'
import { writeFile } from 'node:fs/promises'

const [config, output] = process.argv.slice(2)
if (!config || !output) throw new Error('Usage: vitest-coverage-inventory.mjs CONFIG OUTPUT')
const context = await createVitest('test', { config, watch: false, coverage: { enabled: true } })
try {
  const provider = await context.initCoverageProvider()
  if (!provider || provider.name !== 'v8' || typeof provider.getUntestedFiles !== 'function') {
    throw new Error('Locked V8 provider source-inventory API is unavailable')
  }
  const files = await provider.getUntestedFiles([])
  if (!files.length) throw new Error('Configured coverage source baseline is empty')
  await writeFile(output, JSON.stringify(files.sort().map(file => ({ file }))) + '\n')
} finally {
  await context.close()
}
