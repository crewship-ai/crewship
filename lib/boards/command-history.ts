import { affectedClosure, batchEntities, changedEntities } from './collaboration-plan'
import { entityFromKey, type BoardEntity, type BoardHead, type CollaborativeBatch } from './collaboration-types'
export interface BoardReceipt { seq: number; result_seq: number; placement_revs?: Record<string, number>; element_revs?: Record<string, number>; undo_expires_at?: string }
export interface SavedBoardCommand { before: BoardHead; applied: BoardHead; inverse: CollaborativeBatch; touched: string[]; expiresAt: number }
const copy = <T>(value: T): T => structuredClone(value)
const scene = (head: BoardHead) => head.records.flatMap(record => record.deleted || !record.element ? [] : [record.element])
/** Derive only this command's applied state, never attribute a racing GET's
 * unrelated changes to the local writer. Receipts are server authority. */
export function savedBoardCommand(before: BoardHead, forward: CollaborativeBatch, receipt: BoardReceipt, after: BoardHead): SavedBoardCommand {
  if (!Number.isSafeInteger(receipt.seq) || !Number.isSafeInteger(receipt.result_seq) || forward.base_seq !== before.seq || receipt.result_seq !== before.seq + 1 || receipt.seq < receipt.result_seq) throw new Error('Cannot establish the accepted command sequence')
  const expiresAt = Date.parse(receipt.undo_expires_at ?? '')
  if (!Number.isSafeInteger(expiresAt)) throw new Error('Cannot establish saved undo expiration')
  const elementRev = (id: string, expected: number) => {
    const revision = receipt.element_revs?.[id]
    if (!Number.isSafeInteger(revision) || revision! <= expected) throw new Error('Cannot establish accepted element authority')
    return revision!
  }
  const applied = copy(before); applied.seq = receipt.result_seq
  // History needs geometry and opaque IDs, never cached source titles/URLs.
  applied.placements = applied.placements.map(({ source: _source, ...item }) => item)
  const inverse: CollaborativeBatch = { base_seq: receipt.result_seq, upserts: [], deletes: [], placement_updates: [], placement_deletes: [], placement_restores: [] }
  const records = new Map(applied.records.map(record => [record.element_id, record]))
  const original = new Map(before.records.map(record => [record.element_id, record]))
  for (const item of forward.upserts) {
    const previous = original.get(item.element.id)
    if ((previous?.element_rev ?? 0) !== item.expected_rev) throw new Error('Invalid forward element authority')
    const rev = elementRev(item.element.id, item.expected_rev)
    records.set(item.element.id, { element_id: item.element.id, element_rev: rev, element: copy(item.element), deleted: false })
    if (previous && !previous.deleted && previous.element) inverse.upserts.push({ element: copy(previous.element), expected_rev: rev })
    else inverse.deletes.push({ id: item.element.id, expected_rev: rev })
  }
  for (const item of forward.deletes) {
    const previous = original.get(item.id)
    if (!previous?.element || previous.deleted || previous.element_rev !== item.expected_rev) throw new Error('Invalid forward deletion authority')
    const rev = elementRev(item.id, item.expected_rev)
    records.set(item.id, { element_id: item.id, element_rev: rev, element: null, deleted: true })
    inverse.upserts.push({ element: copy(previous.element), expected_rev: rev })
  }
  applied.records = [...records.values()]
  const pins = new Map(applied.placements.map(item => [item.id, item]))
  const tombstones = new Map((applied.placement_tombstones ?? []).map(item => [item.id, item]))
  const acceptedRev = (id: string, expected: number | undefined) => {
    const revision = receipt.placement_revs?.[id]
    if (expected === undefined || !Number.isSafeInteger(revision) || revision! <= expected) throw new Error('Cannot establish accepted placement authority')
    return revision!
  }
  for (const item of forward.placement_updates ?? []) {
    const previous = pins.get(item.id)
    if (!previous || previous.placement_rev !== item.expected_rev) throw new Error('Invalid forward placement authority')
    const rev = acceptedRev(item.id, item.expected_rev)
    inverse.placement_updates!.push({ id: item.id, expected_rev: rev, x: previous.x, y: previous.y, width: previous.width, height: previous.height })
    pins.set(item.id, { ...previous, x: item.x, y: item.y, width: item.width, height: item.height, placement_rev: rev })
  }
  for (const item of forward.placement_deletes ?? []) {
    const previous = pins.get(item.id)
    if (!previous || previous.placement_rev !== item.expected_rev) throw new Error('Invalid forward placement deletion authority')
    const rev = acceptedRev(item.id, item.expected_rev)
    pins.delete(item.id); tombstones.set(item.id, { id: item.id, placement_rev: rev, deleted: true })
    inverse.placement_restores!.push({ id: item.id, expected_rev: rev })
  }
  for (const item of forward.placement_restores ?? []) {
    if (tombstones.get(item.id)?.placement_rev !== item.expected_rev) throw new Error('Invalid forward placement restore authority')
    const rev = acceptedRev(item.id, item.expected_rev)
    const restored = after.placements.find(pin => pin.id === item.id && pin.placement_rev === rev)
    // Tombstones intentionally reveal no geometry. A raced read cannot supply
    // proof of the restored geometry; conservatively make this undo unavailable.
    if (!restored) throw new Error('Restored placement changed before it could be recorded')
    const { source: _source, ...geometry } = restored
    pins.set(item.id, copy(geometry)); tombstones.delete(item.id)
    inverse.placement_deletes!.push({ id: item.id, expected_rev: rev })
  }
  applied.placements = [...pins.values()]; applied.placement_tombstones = [...tombstones.values()]
  const sanitizedBefore = copy(before); sanitizedBefore.placements = sanitizedBefore.placements.map(({ source: _source, ...item }) => item)
  return { before: sanitizedBefore, applied, inverse, touched: [...batchEntities(forward)], expiresAt }
}
export function planSavedUndo(command: SavedBoardCommand, latest: BoardHead, now = Date.now()): { kind: 'expired' } | { kind: 'ready'; batch: CollaborativeBatch } | { kind: 'conflict'; conflicts: BoardEntity[] } {
  if (now >= command.expiresAt) return { kind: 'expired' }
  if (latest.seq < command.applied.seq) throw new Error('Latest board predates the saved command')
  const closure = affectedClosure(command.touched, [scene(command.before), scene(command.applied), scene(latest)])
  const conflicts = [...changedEntities(command.applied, latest)].filter(id => closure.has(id)).map(entityFromKey)
  if (conflicts.length) return { kind: 'conflict', conflicts }
  return { kind: 'ready', batch: { ...copy(command.inverse), base_seq: latest.seq } }
}
