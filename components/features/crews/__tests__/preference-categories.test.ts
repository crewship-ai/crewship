import { describe, expect, it } from 'vitest'
import { preferenceCategory, preferenceLabel } from '../preference-categories'

describe('stable preference categories', () => {
  it('uses the same category icon for arbitrary new titles in a known namespace', () => {
    for (const key of ['communication.new_notice', 'communication:another_new_title', 'communication/unseen']) {
      expect(preferenceCategory(key)).toBe(preferenceCategory('contact'))
      expect(preferenceCategory(key).icon).toBeDefined()
    }
    expect(preferenceLabel('communication.new_notice')).toBe('New notice')
  })
  it('keeps unknown categories and future keys visible with an Other icon', () => {
    for (const key of ['something_never_seen', 'future.custom_field', '', 'constructor', '__proto__']) {
      expect(preferenceCategory(key).id).toBe('other')
      expect(preferenceCategory(key).icon).toBeDefined()
    }
    expect(preferenceLabel('future.custom_field')).toBe('Future.custom field')
  })
  it('covers all currently extracted fields and legacy preferences', () => {
    for (const key of ['role', 'owns', 'constraint', 'process', 'prefers', 'tooling', 'timezone', 'language', 'contact']) {
      expect(preferenceCategory(key).id).not.toBe('other')
    }
    expect(preferenceCategory('styl_odpovedi').id).toBe('communication')
    expect(preferenceLabel('styl_odpovedi')).toBe('Styl odpovědí')
  })
})
