import { manualBoardBatch } from './manual-batch'
import { entityFromKey, elementStamp, placementStamp, type BoardDraft, type BoardEntity, type BoardHead, type CollaborativeBatch } from './collaboration-types'
import type { BoardElement } from './revision-adapter'
const clone = <T>(value: T): T => structuredClone(value)
const elementKey = (id: string) => `element:${id}`
const placementKey = (id: string) => `placement:${id}`
function references(element: BoardElement): string[] {
  const result: string[] = []
  for (const field of ['frameId', 'containerId']) if (typeof element[field] === 'string') result.push(element[field] as string)
  for (const field of ['startBinding', 'endBinding']) {
    const binding = element[field] as { elementId?: unknown } | null | undefined
    if (typeof binding?.elementId === 'string') result.push(binding.elementId)
  }
  if (Array.isArray(element.boundElements)) for (const binding of element.boundElements) if (binding && typeof binding.id === 'string') result.push(binding.id)
  return result
}
/** Includes both directions, old and new graphs, so deleting a parent or
 * adding a new remote arrow to a locally edited node cannot evade conflict. */
export function affectedClosure(seeds: Iterable<string>, scenes: readonly (readonly BoardElement[])[]): Set<string> {
  const graph = new Map<string, Set<string>>()
  const groups = new Map<string, Set<string>>()
  const connect = (a: string, b: string) => {
    if (!graph.has(a)) graph.set(a, new Set()); if (!graph.has(b)) graph.set(b, new Set())
    graph.get(a)!.add(b); graph.get(b)!.add(a)
  }
  for (const scene of scenes) for (const element of scene) {
    const id = elementKey(element.id)
    for (const reference of references(element)) connect(id, elementKey(reference))
    if (Array.isArray(element.groupIds)) for (const group of element.groupIds) if (typeof group === 'string') {
      const members = groups.get(group) ?? new Set<string>(); members.add(id); groups.set(group, members)
    }
  }
  for (const members of groups.values()) {
    const [first, ...rest] = members
    for (const member of rest) connect(first, member)
  }
  const result = new Set(seeds)
  const queue = [...result]
  for (let index = 0; index < queue.length; index++) for (const related of graph.get(queue[index]) ?? []) if (!result.has(related)) { result.add(related); queue.push(related) }
  return result
}
const scene = (head: BoardHead) => head.records.flatMap(record => !record.deleted && record.element ? [record.element] : [])
export function changedEntities(before: BoardHead, after: BoardHead): Set<string> {
  const result = new Set<string>()
  const old = new Map(before.records.map(record => [record.element_id, record]))
  const next = new Map(after.records.map(record => [record.element_id, record]))
  for (const id of new Set([...old.keys(), ...next.keys()])) {
    const a = old.get(id), b = next.get(id)
    if (a?.element_rev !== b?.element_rev || a?.deleted !== b?.deleted || elementStamp(a?.element) !== elementStamp(b?.element)) result.add(elementKey(id))
  }
  const placements = (head: BoardHead) => new Map([...head.placements.map(item => [item.id, { rev: item.placement_rev, geometry: placementStamp(item) }] as const), ...(head.placement_tombstones ?? []).map(item => [item.id, { rev: item.placement_rev, geometry: null }] as const)])
  const a = placements(before), b = placements(after)
  for (const id of new Set([...a.keys(), ...b.keys()])) if (a.get(id)?.rev !== b.get(id)?.rev || a.get(id)?.geometry !== b.get(id)?.geometry) result.add(placementKey(id))
  return result
}
export function draftBatch(base: BoardHead, draft: BoardDraft): CollaborativeBatch {
  const batch: CollaborativeBatch = manualBoardBatch(base, draft.elements)
  const local = new Map(draft.placements.map(item => [item.id, item]))
  batch.placement_updates = []
  batch.placement_deletes = []
  for (const placement of base.placements) {
    const next = local.get(placement.id)
    const expected = placement.placement_rev === undefined ? {} : { expected_rev: placement.placement_rev }
    if (!next) batch.placement_deletes.push({ id: placement.id, ...expected })
    else if (placementStamp(placement) !== placementStamp(next)) batch.placement_updates.push({ id: placement.id, x: next.x, y: next.y, width: next.width, height: next.height, ...expected })
  }
  // Adding/repointing pins requires the separate authorized source operation.
  if (draft.placements.some(item => !base.placements.some(saved => saved.id === item.id))) throw new Error('New artifact pins require an explicit pin operation')
  return batch
}
export function batchEntities(batch: CollaborativeBatch): Set<string> {
  return new Set([...batch.upserts.map(item => elementKey(item.element.id)), ...batch.deletes.map(item => elementKey(item.id)), ...(batch.placement_updates ?? []).map(item => placementKey(item.id)), ...(batch.placement_deletes ?? []).map(item => placementKey(item.id)), ...(batch.placement_restores ?? []).map(item => placementKey(item.id)), ...(batch.placement_modes ?? []).map(item => placementKey(item.id))])
}
export interface BoardMergePlan { kind: 'merged' | 'conflict' | 'stale'; canonical: BoardHead; draft: BoardDraft; latest: BoardHead; conflicts: BoardEntity[] }
export function planBoardMerge(base: BoardHead, draft: BoardDraft, latest: BoardHead): BoardMergePlan {
  if (latest.seq < base.seq) return { kind: 'stale', canonical: clone(base), draft: clone(draft), latest: clone(latest), conflicts: [] }
  const changed = changedEntities(base, latest)
  if (latest.seq === base.seq && changed.size) throw new Error('Canonical board sequence reused for different geometry')
  const batch = draftBatch(base, draft)
  const touched = batchEntities(batch)
  const closure = affectedClosure(touched, [scene(base), draft.elements, scene(latest)])
  const conflicts = [...changed].filter(id => closure.has(id))
  if (latest.seq !== base.seq) for (const placement of base.placements) if (placement.placement_rev === undefined && touched.has(placementKey(placement.id))) conflicts.push(placementKey(placement.id))
  const authorized = new Map(latest.placements.map(item => [item.id, item]))
  const redact = (items: readonly typeof base.placements[number][]) => items.map(item => {
    const { source: _source, ...geometry } = item
    const source = authorized.get(item.id)?.source
    return { ...geometry, ...(source ? { source: clone(source) } : {}) }
  })
  if (conflicts.length) return { kind: 'conflict', canonical: clone(base), latest: clone(latest), draft: { elements: clone(draft.elements), placements: redact(draft.placements) }, conflicts: [...new Set(conflicts)].map(entityFromKey) }
  const elements = new Map(scene(latest).map(element => [element.id, clone(element)]))
  const local = new Map(draft.elements.map(element => [element.id, element]))
  for (const id of touched) if (id.startsWith('element:')) {
    const raw = id.slice('element:'.length)
    const element = local.get(raw)
    if (element) elements.set(raw, clone(element)); else elements.delete(raw)
  }
  const placements = new Map(latest.placements.map(item => [item.id, clone(item)]))
  const localPins = new Map(redact(draft.placements).map(item => [item.id, item]))
  for (const id of touched) if (id.startsWith('placement:')) {
    const raw = id.slice('placement:'.length)
    const item = localPins.get(raw)
    if (item) placements.set(raw, item); else placements.delete(raw)
  }
  return { kind: 'merged', canonical: clone(latest), latest: clone(latest), conflicts: [], draft: { elements: [...elements.values()].sort((a, b) => String(a.index ?? a.id) < String(b.index ?? b.id) ? -1 : String(a.index ?? a.id) > String(b.index ?? b.id) ? 1 : 0), placements: [...placements.values()] } }
}
