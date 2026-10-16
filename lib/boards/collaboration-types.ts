import type { BoardElement, BoardBatch } from './revision-adapter'
import type { PageBoard, BoardPlacement } from '../../hooks/use-page-board'
export interface VersionedPlacement extends BoardPlacement { placement_rev?: number }
export interface BoardHead extends Omit<PageBoard, 'placements'> {
  placements: VersionedPlacement[]
  placement_tombstones?: { id: string; placement_rev: number; deleted: true }[]
}
export interface BoardDraft { elements: readonly BoardElement[]; placements: readonly VersionedPlacement[] }
export interface CollaborativeBatch extends Omit<BoardBatch, 'placement_updates' | 'placement_deletes'> {
  placement_updates?: { id: string; expected_rev?: number; x: number; y: number; width: number; height: number }[]
  placement_deletes?: { id: string; expected_rev?: number }[]
  placement_restores?: { id: string; expected_rev: number }[]
}
export interface BoardEntity { kind: 'element' | 'placement'; id: string }
export const entityKey = (entity: BoardEntity) => `${entity.kind}:${entity.id}`
export const entityFromKey = (key: string): BoardEntity => ({ kind: key.startsWith('element:') ? 'element' : 'placement', id: key.slice(key.indexOf(':') + 1) })
/** Semantic comparison excludes editor clocks, never server authority. */
export const elementStamp = (element: BoardElement | null | undefined) => element == null ? null : JSON.stringify(Object.fromEntries(Object.entries({ ...element, boundElements: element.boundElements ?? [] }).filter(([key]) => !['version', 'versionNonce', 'updated'].includes(key)).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0)))
export const placementStamp = (placement: BoardPlacement | undefined) => placement ? JSON.stringify([placement.x, placement.y, placement.width, placement.height]) : null
