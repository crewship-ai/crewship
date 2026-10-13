/** Excalidraw metadata is presentation data. Only server revisions authorize writes. */
export interface BoardElement { readonly id: string; readonly isDeleted?: boolean; readonly [key: string]: unknown }
export interface BoardRecord { element: BoardElement; revision: number }
export interface BoardSnapshot { seq: number; records: BoardRecord[] }
export interface BoardBatch {
  base_seq: number
  upserts: { element: BoardElement; expected_rev: number }[]
  deletes: { id: string; expected_rev: number }[]
}
const copy = <T>(value: T): T => structuredClone(value)
const stamp = (element: BoardElement | undefined) => JSON.stringify(element ? Object.fromEntries(Object.entries({ ...element, ...(element.type ? { boundElements: element.boundElements ?? [] } : {}) }).filter(([key]) => !['version', 'versionNonce', 'updated'].includes(key)).sort(([a], [b]) => a.localeCompare(b))) : null)

/** Experimental integration adapter, not a production collaboration engine.
 * Requires serialized canonical delivery and history.clear() on EVERY remote
 * change. It does not solve delayed/out-of-order callbacks or selective undo.
 * Rejected proposals remain available during canonical repair. */
export class BoardRevisionAdapter {
  private observed: string
  private snapshot: BoardSnapshot
  proposal: BoardBatch | null = null
  constructor(snapshot: BoardSnapshot) { this.snapshot = copy(snapshot); this.observed = this.sceneStamp(snapshot.records.map(item => item.element)) }
  private sceneStamp(elements: readonly BoardElement[]) { return elements.map(stamp).sort().join('\n') }
  canonical() { return copy(this.snapshot) }
  receive(snapshot: BoardSnapshot) {
    if (snapshot.seq < this.snapshot.seq) throw new Error('Board sequence moved backwards')
    this.snapshot = copy(snapshot)
  }
  prepare(elements: readonly BoardElement[]): BoardBatch {
    const signature = this.sceneStamp(elements)
    const batch: BoardBatch = { base_seq: this.snapshot.seq, upserts: [], deletes: [] }
    // onChange also repeats the previous render during asynchronous updates.
    // Repeated observed content is not a fresh authorized user gesture.
    if (signature === this.observed) return batch
    this.observed = signature
    const records = new Map(this.snapshot.records.map(record => [record.element.id, record]))
    const input = new Map(elements.map(element => [element.id, element]))
    for (const [id, element] of input) {
      const previous = records.get(id)
      if (stamp(element) === stamp(previous?.element)) continue
      const expected = previous?.revision ?? 0
      if (element.isDeleted) { if (previous && !previous.element.isDeleted) batch.deletes.push({ id, expected_rev: expected }) }
      else batch.upserts.push({ element: copy(element), expected_rev: expected })
    }
    for (const [id, previous] of records) {
      if (!input.has(id) && !previous.element.isDeleted) batch.deletes.push({ id, expected_rev: previous.revision })
    }
    return batch
  }
  accepted(_batch: BoardBatch, snapshot: BoardSnapshot) {
    this.receive(snapshot); this.proposal = null
  }
  rejected(batch: BoardBatch, snapshot: BoardSnapshot) {
    this.proposal = copy(batch)
    this.receive(snapshot)
    // The host must also clear public editor history after repair.
  }
}
