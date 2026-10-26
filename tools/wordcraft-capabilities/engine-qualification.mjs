import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'

const hash = value => createHash('sha256').update(value).digest('hex')
const hostSha = '96f3ca3a5c21e480ff6fbc01f289e317e285ed13'
const patchSha = '9c2ff901ae408c72d12bbb864895a63791f000551978bf615734a6b41e381581'
const checks = ['execution', 'ownUndo', 'restoreP', 'staleTarget']
const fixturePath = 'tools/wordcraft-capabilities/c1-qualification/fixtures.json'
const testPath = 'tools/wordcraft-capabilities/c1-qualification/qualification.rs'

/** Engine fixture evidence is separate from product qualification. It never grants
 * participant permissions or activates a UI command. Any source drift stops generation. */
export function applyEngineQualifications(catalog, policy, profilesRaw, readSource) {
  const fail = () => { throw Error('engine qualification policy drift: rerun actual isolated C1 matrix') }
  if (policy.schemaVersion !== 1 || policy.catalogVersion !== 4 || policy.engineSha !== catalog.engine.sha
    || policy.c1HostSha !== hostSha || policy.helperPatchSha256 !== patchSha
    || policy.registryDigest !== catalog.registryDigest || policy.profileSha256 !== hash(profilesRaw)
    || policy.scope !== 'isolated-c1-fixture') fail()
  if (policy.testSource?.path !== testPath || policy.fixturesSource?.path !== fixturePath) fail()
  for (const source of [policy.testSource, policy.fixturesSource]) {
    if (hash(readSource(source.path)) !== source.sha256) fail()
  }
  const fixtures = JSON.parse(readSource(fixturePath))
  const ids = Object.keys(policy.commands)
  if (!ids.length || ids.length > 8) fail()
  const known = new Map(catalog.commands.map(command => [command.id, command]))
  for (const id of ids) {
    const entry = policy.commands[id], fixture = fixtures[id], command = known.get(id)
    if (!fixture || !command || command.permanentlyForbidden || command.schemaStatus !== 'exact'
      || !['text-enter', 'character-format', 'paragraphs', 'lists'].includes(command.group)) fail()
    const properties = Object.fromEntries(Object.entries(fixture.params).map(([key, value]) => [key, { type: typeof value, enum: [value] }]))
    const expectedSchema = { type: 'object', properties, required: Object.keys(fixture.params), additionalProperties: false }
    if (entry.test !== 'ce_' + id.replaceAll('.', '_').toLowerCase()
      || entry.coverage !== 'tested-fixture-only' || entry.undoCommand !== 'participant.undo'
      || entry.targetProfile !== 'exact-current-revision/body/utf8-grapheme-boundaries'
      || !isDeepStrictEqual(entry.invariants, checks)
      || !isDeepStrictEqual(entry.testedParameters, fixture.params)
      || !isDeepStrictEqual(entry.testedTarget, { unit: 'utf8_bytes', start: fixture.start, end: fixture.end })
      || !isDeepStrictEqual(entry.testedParameterSchema, expectedSchema)) fail()
  }
  return { ...catalog, version: `wordcraft:${catalog.engine.sha}:4`, commands: catalog.commands.map(command => ({
    ...command, ...(policy.commands[command.id] ? { engineQualification: {
      status: 'qualified', scope: policy.scope, engineSha: policy.engineSha, c1HostSha: policy.c1HostSha,
      helperPatchSha256: policy.helperPatchSha256, testSource: policy.testSource,
      fixturesSource: policy.fixturesSource, ...policy.commands[command.id],
    } } : {}),
  })) }
}
