// @vitest-environment node
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { classify, generate, parameterSchema } from './generate.mjs'
import catalog from '../../schemas/wordcraft/capabilities.json'

const registry = JSON.parse(readFileSync(new URL('./registry.json', import.meta.url), 'utf8'))

describe('pinned WordCraft registry catalog', () => {
  it('matches the mechanically exported registry, including every parameter descriptor', () => {
    expect(generate(registry, catalog.sourceIdentity)).toEqual(catalog)
    execFileSync(process.execPath, ['tools/wordcraft-capabilities/generate.mjs', '--check'])
  })
  it('detects added commands and parameter changes instead of silently qualifying them', () => {
    expect(generate([...registry, { id: 'new.command', label: '', location: '', params: '{}', shortcut: '', mutates: true }], catalog.sourceIdentity)).not.toEqual(catalog)
    expect(generate(registry.map((c: {id: string}) => c.id === 'text.insert' ? { ...c, params: '{"different": string}' } : c), catalog.sourceIdentity)).not.toEqual(catalog)
    expect(() => generate([...registry, registry[0]], catalog.sourceIdentity)).toThrow('duplicate')
  })
  it('qualifies nothing, and never treats a pure flag as permission', () => {
    expect(catalog.commands.length).toBeGreaterThan(100)
    expect(catalog.commands.every(c => c.qualification === 'unqualified')).toBe(true)
    for (const id of ['file.open', 'font.add', 'review.addToDictionary', 'review.ignoreAll', 'tools.macros']) {
      expect(catalog.commands.find(c => c.id === id)).toMatchObject({ permanentlyForbidden: true, group: 'process-global' })
    }
    expect(classify({ id: 'unknown.export', location: '', mutates: false }).effect).toBe('non-document-unreviewed')
  })
  it('groups the paragraph list commands with lists', () => {
    for (const id of ['para.bullets', 'para.numbering', 'para.defineBullet', 'para.listLevel', 'para.restartNumbering', 'para.setNumberingValue']) {
      expect(catalog.commands.find(c => c.id === id)?.group).toBe('lists')
    }
    expect(catalog.commands.find(c => c.id === 'review.reply')?.group).toBe('comments')
  })
  it('produces exact closed schemas only for descriptions it understands', () => {
    expect(parameterSchema('{"text": string}')).toEqual({ status: 'exact', schema: { type: 'object', properties: { text: { type: 'string' } }, required: ['text'], additionalProperties: false } })
    expect(parameterSchema('{"value"?: bool (omit to toggle)}')).toEqual({ status: 'unresolved', schema: false })
    expect(parameterSchema('{}').schema).toMatchObject({ additionalProperties: false })
  })
})
