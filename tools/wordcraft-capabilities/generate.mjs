#!/usr/bin/env node
/** Generate from the actual compiled registry, never from a hand-written command list. */
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

import { applyEngineQualifications } from './engine-qualification.mjs'

export const ENGINE_SHA = '7ef2633a7e84956522d1ca47b99fe6feef16c1db'
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const digest = value => createHash('sha256').update(value).digest('hex')
const profiles = JSON.parse(readFileSync(resolve(root, 'tools/wordcraft-capabilities/parameter-profiles.json'), 'utf8'))
const git = (source, ...args) => execFileSync('git', ['--no-replace-objects', '-C', source, ...args], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 })

export function sourceIdentity(source) {
  if (git(source, 'rev-parse', 'HEAD').trim() !== ENGINE_SHA) throw Error('engine SHA mismatch')
  if (git(source, 'status', '--porcelain', '--untracked-files=all').trim()) throw Error('engine source drift: pinned sources must be clean')
  const files = git(source, 'ls-tree', '-r', '--name-only', ENGINE_SHA, 'crates/engine/src', 'crates/doc/src').trim().split('\n')
  // Record all engine and document sources, not just the top-level registry.
  return files.map(path => ({ path, sha256: digest(readFileSync(resolve(source, path))) }))
}

export function classify(command) {
  const { id, location, mutates } = command
  if (/^(file\.|font\.|tools\.|options\.|templates\.|userTemplates\.)/.test(id) || /dictionary|ignoreAll|macro/i.test(id)) {
    return { group: 'process-global', effect: 'process-global', permanentlyForbidden: true }
  }
  let group = 'unclassified'
  if (/^text\./.test(id) || /^caret\./.test(id) || /^edit\./.test(id)) group = 'text-enter'
  else if (/^format\./.test(id)) group = 'character-format'
  else if (/^para\.(bullets|numbering|defineBullet|listLevel|restartNumbering|setNumberingValue)/.test(id)) group = 'lists'
  else if (/^para\./.test(id)) group = 'paragraphs'
  else if (/^list[s]?\./.test(id)) group = 'lists'
  else if (/table/i.test(id) || /Tables/.test(location)) group = 'tables'
  else if (/picture|image/i.test(id)) group = 'images'
  else if (/comment/i.test(id) || /Comments/.test(location)) group = 'comments'
  else if (/revision|trackChange/i.test(id) || /Tracking|Changes/.test(location)) group = 'tracked-changes'
  // `pure` only means non-document. It does NOT prove absence of I/O or global effects.
  return { group, effect: mutates ? 'document-candidate' : 'non-document-unreviewed', permanentlyForbidden: false }
}

/** The upstream parameter descriptions are prose, not JSON Schema. Translate only
 * an exact, lossless subset. Everything else rejects all values until reviewed. */
export function parameterSchema(descriptor) {
  if (descriptor === '{}') return { status: 'exact', schema: { type: 'object', properties: {}, additionalProperties: false } }
  if (!descriptor.startsWith('{') || !descriptor.endsWith('}')) return { status: 'unresolved', schema: false }
  const fields = descriptor.slice(1, -1).split(',').map(value => value.trim())
  const properties = {}, required = []
  for (const field of fields) {
    const match = /^"([\w]+)"(\?)?\s*:\s*(string|number|bool|boolean|integer)$/.exec(field)
    if (!match || match[1] in properties) return { status: 'unresolved', schema: false }
    properties[match[1]] = { type: match[3] === 'bool' ? 'boolean' : match[3] }
    if (!match[2]) required.push(match[1])
  }
  return { status: 'exact', schema: { type: 'object', properties, required, additionalProperties: false } }
}

export function generate(registry, identity) {
  if (profiles.engineSha !== ENGINE_SHA) throw Error('parameter profile engine mismatch')
  const registered = new Map(registry.map(command => [command.id, command]))
  for (const [id, profile] of Object.entries(profiles.commands)) {
    const command = registered.get(id)
    if (!command || digest(command.params) !== profile.descriptorSha256) throw Error(`parameter profile registry drift: ${id}`)
    if (classify(command).permanentlyForbidden) throw Error('forbidden command cannot receive a parameter profile')
    for (const source of profile.sourceFiles) {
      if (!identity.some(file => file.path === source.path && file.sha256 === source.sha256)) throw Error(`parameter profile source drift: ${id}`)
    }
  }
  const seen = new Set()
  const commands = registry.map(command => {
    if (typeof command.id !== 'string' || seen.has(command.id) || typeof command.params !== 'string' || typeof command.mutates !== 'boolean') throw Error('invalid or duplicate registry command')
    seen.add(command.id)
    const profile = profiles.commands[command.id]
    const parameters = profile ? { status: 'exact', schema: profile.parameterSchema } : parameterSchema(command.params)
    return { id: command.id, label: command.label, location: command.location, shortcut: command.shortcut,
      parameterDescriptor: command.params, parameterSchema: parameters.schema, schemaStatus: parameters.status,
      ...(profile ? { schemaCoverage: profile.coverage, schemaEvidence: profile.sourceFiles } : {}),
      ...classify(command), qualification: 'unqualified' }
  }).sort((a, b) => a.id.localeCompare(b.id, 'en'))
  const catalog = { schemaVersion: 1, engine: { repository: 'https://github.com/storytold/wordcraft', sha: ENGINE_SHA },
    version: `wordcraft:${ENGINE_SHA}:${profiles.profileVersion}`, sourceIdentity: identity, registryDigest: digest(JSON.stringify(registry)), commands }
  const policyPath = resolve(root, 'tools/wordcraft-capabilities/engine-qualification-policy.json')
  return existsSync(policyPath) ? applyEngineQualifications(catalog, JSON.parse(readFileSync(policyPath, 'utf8')), readFileSync(resolve(root, 'tools/wordcraft-capabilities/parameter-profiles.json')), path => readFileSync(resolve(root, path))) : catalog
}

export function main(args) {
  const option = name => args.includes(name) ? args[args.indexOf(name) + 1] : undefined
  const source = option('--source')
  const target = source && resolve(option('--target-dir') ?? resolve(source, 'target'))
  const output = resolve(option('--output') ?? resolve(root, 'schemas/wordcraft/capabilities.json'))
  const snapshot = resolve(root, 'tools/wordcraft-capabilities/registry.json')
  const identityPath = resolve(root, 'tools/wordcraft-capabilities/source-identity.json')
  const identity = source ? sourceIdentity(source) : JSON.parse(readFileSync(identityPath, 'utf8'))
  if (source) execFileSync('cargo', ['build', '--locked', '--manifest-path', resolve(source, 'Cargo.toml'), '--target-dir', target, '-p', 'wordcraft-cli'], { stdio: 'inherit' })
  const raw = source ? execFileSync(resolve(target, 'debug/wordcraft-cli'), ['commands', '--json'], { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024, env: { ...process.env, LANG: 'C.UTF-8' } }) : readFileSync(snapshot, 'utf8')
  const registry = JSON.parse(raw)
  const generated = JSON.stringify(generate(registry, identity), null, 2) + '\n'
  if (args.includes('--check')) {
    if (readFileSync(output, 'utf8') !== generated) throw Error('registry/catalog drift: regenerate and requalify changed commands')
    if (source && JSON.stringify(identity) !== JSON.stringify(JSON.parse(readFileSync(identityPath, 'utf8')))) throw Error('engine source drift')
  } else {
    if (!source) throw Error('Regeneration requires --source; offline mode is check-only')
    writeFileSync(snapshot, JSON.stringify(registry, null, 2) + '\n')
    writeFileSync(identityPath, JSON.stringify(identity, null, 2) + '\n')
    writeFileSync(output, generated)
  }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main(process.argv.slice(2))
