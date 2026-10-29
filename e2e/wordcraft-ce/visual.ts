import { expect, type Page } from '@playwright/test'
import { canvas } from './contract'

export interface Rect { x: number; y: number; width: number; height: number }
export function featureCrops(feature: string): { writer: Rect; peer: Rect } {
  const raw = process.env.WORDCRAFT_CE_FEATURE_CROPS
  if (!raw) throw Error('WORDCRAFT_CE_FEATURE_CROPS must explicitly select caret-free document glyph regions; whole-canvas fallback is forbidden')
  const value = JSON.parse(raw)[feature] as { writer: Rect; peer: Rect } | undefined
  if (!value) throw Error(`Missing glyph crop for feature ${feature}`)
  for (const rect of [value.writer, value.peer]) {
    if (!rect || ![rect.x, rect.y, rect.width, rect.height].every(Number.isFinite) || rect.x < 0 || rect.y < 0 || rect.width <= 0 || rect.height <= 0) throw Error('Invalid glyph crop')
  }
  expect(value.writer.width).toBe(value.peer.width)
  expect(value.writer.height).toBe(value.peer.height)
  return value
}
export async function glyphImage(page: Page, relative: Rect): Promise<Buffer> {
  const bounds = await canvas(page).boundingBox()
  if (!bounds || relative.x + relative.width > bounds.width || relative.y + relative.height > bounds.height) throw Error('Glyph crop must be inside the visible native canvas')
  return page.screenshot({ type: 'png', clip: { ...relative, x: bounds.x + relative.x, y: bounds.y + relative.y } })
}
export async function stableGlyphImage(page: Page, relative: Rect) {
  const image = await glyphImage(page, relative)
  await new Promise(resolve => setTimeout(resolve, 600)) // Outside timed samples; catch common caret/presence instability.
  expect((await glyphImage(page, relative)).equals(image), 'Crop must exclude caret, selection, presence and other unstable pixels').toBe(true)
  return image
}
