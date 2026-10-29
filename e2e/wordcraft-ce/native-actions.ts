import type { Page } from '@playwright/test'
import { canvas } from './contract'

export type NativeAction = { click: { x: number; y: number } } | { press: string } | { type: string }
/** Calibrated native egui UI input only. No command API, WASM calls or DOM
 * mutation. The journey independently requires the exact command and effect. */
export function parseNativeActions(raw: string | undefined, feature: string): NativeAction[] {
  if (!raw) throw Error('WORDCRAFT_CE_NATIVE_ACTIONS must specify real native ribbon UI actions; no synthetic DOM controls')
  const value = JSON.parse(raw)?.[feature] as unknown
  if (!Array.isArray(value) || !value.length || value.length > 32) throw Error(`Missing or invalid native UI actions for ${feature}`)
  for (const item of value) {
    if (!item || typeof item !== 'object' || Array.isArray(item) || Object.keys(item).length !== 1) throw Error('Invalid native UI action')
    if ('click' in item) {
      const point = item.click
      if (!point || typeof point !== 'object' || Object.keys(point).sort().join(',') !== 'x,y' || !Number.isFinite(point.x) || !Number.isFinite(point.y) || point.x < 0 || point.y < 0) throw Error('Invalid canvas UI point')
    } else if ('press' in item) {
      if (typeof item.press !== 'string' || !item.press.length || item.press.length > 100) throw Error('Invalid native UI key')
    } else if ('type' in item) {
      if (typeof item.type !== 'string' || !item.type.length || item.type.length > 200) throw Error('Invalid native UI text')
    } else throw Error('Unsupported native UI action')
  }
  return value as NativeAction[]
}
export async function applyNativeAction(page: Page, feature: string) {
  for (const action of parseNativeActions(process.env.WORDCRAFT_CE_NATIVE_ACTIONS, feature)) {
    if ('click' in action) {
      const bounds = await canvas(page).boundingBox()
      if (!bounds || action.click.x >= bounds.width || action.click.y >= bounds.height) throw Error('Native UI click must lie inside the visible canvas')
      await canvas(page).click({ position: action.click })
    } else if ('press' in action) await page.keyboard.press(action.press)
    else await page.keyboard.type(action.type)
  }
}
