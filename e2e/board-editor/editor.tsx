import React from 'react'
import { createRoot } from 'react-dom/client'
import { Excalidraw, CaptureUpdateAction, convertToExcalidrawElements, newElementWith, MainMenu } from '@excalidraw/excalidraw'
import type { ExcalidrawImperativeAPI } from '@excalidraw/excalidraw/types'
import '@excalidraw/excalidraw/index.css'
import { BoardRevisionAdapter, type BoardBatch, type BoardSnapshot, type BoardElement } from '../../lib/boards/revision-adapter'

// Deliberately test-only canonical store. This proves adapter/editor behavior,
// not HTTP authorization, persistence, websocket delivery or production ACLs.
const seed = convertToExcalidrawElements([{ type: 'rectangle', id: 'shape', groupIds: ['pair'], x: 50, y: 50, width: 120, height: 80 }, { type: 'ellipse', id: 'other', groupIds: ['pair'], x: 230, y: 50, width: 100, height: 80 }], { regenerateIds: false })
let canonical: BoardSnapshot = { seq: 1, records: seed.map(element => ({ element, revision: 1 })) }
const adapters = [new BoardRevisionAdapter(canonical), new BoardRevisionAdapter(canonical)]
const apis: ExcalidrawImperativeAPI[] = []
const journal: BoardBatch[] = []
const rejected: { editor: number; batch: BoardBatch }[] = []
let denyNext = false
let clears = 0
const snapshot = () => structuredClone(canonical)
function apply(batch: BoardBatch): boolean {
  journal.push(structuredClone(batch))
  const records = new Map(canonical.records.map(record => [record.element.id, record]))
  const changes = [...batch.upserts.map(item => ({ id: item.element.id, expected: item.expected_rev })), ...batch.deletes.map(item => ({ id: item.id, expected: item.expected_rev }))]
  if (denyNext || batch.base_seq !== canonical.seq || changes.some(item => (records.get(item.id)?.revision ?? 0) !== item.expected)) { denyNext = false; return false }
  for (const item of batch.upserts) records.set(item.element.id, { element: structuredClone(item.element), revision: item.expected_rev + 1 })
  for (const item of batch.deletes) { const previous = records.get(item.id)!; records.set(item.id, { element: { ...previous.element, isDeleted: true }, revision: previous.revision + 1 }) }
  canonical = { seq: canonical.seq + 1, records: [...records.values()] }; return true
}
function sync(editor: number, clear = true) {
  adapters[editor].receive(canonical)
  apis[editor].updateScene({ elements: canonical.records.map(item => item.element) as ReturnType<typeof convertToExcalidrawElements>, captureUpdate: CaptureUpdateAction.NEVER })
  if (clear) { apis[editor].history.clear(); clears++ }
}
function changed(editor: number, elements: readonly BoardElement[]) {
  if (!apis[editor]) return
  const batch = adapters[editor].prepare(elements)
  if (!batch.upserts.length && !batch.deletes.length) return
  if (apply(batch)) {
    adapters[editor].accepted(batch, canonical)
    for (let i = 0; i < 2; i++) if (i !== editor && apis[i]) sync(i)
  } else {
    rejected.push({ editor, batch }); adapters[editor].rejected(batch, canonical); sync(editor, true)
  }
}
const gate = {
  ready: () => apis.filter(Boolean).length === 2,
  snapshot,
  journal: () => structuredClone(journal),
  rejected: () => structuredClone(rejected),
  proposal: (editor: number) => structuredClone(adapters[editor].proposal),
  clears: () => clears,
  scene: (editor: number) => apis[editor].getSceneElementsIncludingDeleted(),
  acknowledge: (editor: number) => { apis[editor].history.clear() },
  deny: () => { denyNext = true },
  move: (editor: number, id: string, x: number, y?: number) => {
    const api = apis[editor]
    api.updateScene({ elements: api.getSceneElementsIncludingDeleted().map(element => element.id === id ? newElementWith(element, { x, ...(y === undefined ? {} : { y }) }) : element), captureUpdate: CaptureUpdateAction.IMMEDIATELY })
  },
  deleteGroup: (editor: number) => {
    const api = apis[editor]
    api.updateScene({ elements: api.getSceneElementsIncludingDeleted().map(element => newElementWith(element, { isDeleted: true })), captureUpdate: CaptureUpdateAction.IMMEDIATELY })
  },
  compatibility: () => convertToExcalidrawElements([
    { type: 'rectangle', id: 'rect', x: 0, y: 0, width: 100, height: 100, label: { text: 'Bound label' } },
    { type: 'ellipse', id: 'ellipse', x: 200, y: 0, width: 100, height: 100 },
    { type: 'diamond', id: 'diamond', x: 400, y: 0, width: 100, height: 100 },
    { type: 'text', id: 'text', x: 0, y: 200, text: 'Independent text' },
    { type: 'line', id: 'line', x: 200, y: 200, points: [[0, 0], [100, 50]] },
    { type: 'arrow', id: 'arrow', x: 0, y: 0, start: { id: 'rect' }, end: { id: 'ellipse' } },
    { type: 'frame', id: 'frame', x: 0, y: 400, width: 500, height: 200, children: [] },
  ], { regenerateIds: false }),
}
;(window as unknown as { boardGate: typeof gate }).boardGate = gate
function App() { return <><p>Isolated integration gate · test-only canonical store · history resets after rejected changes</p><div style={{ display: 'flex', height: 500 }}>{[0, 1].map(editor => <div key={editor} data-editor={editor} style={{ width: '50%', border: '1px solid gray' }}><Excalidraw initialData={{ elements: seed }} excalidrawAPI={api => { apis[editor] = api }} onChange={elements => changed(editor, elements)} aiEnabled={false} validateEmbeddable={false} onLinkOpen={(_element, event) => event.preventDefault()} UIOptions={{ canvasActions: { loadScene: false, saveToActiveFile: false, export: false, saveAsImage: false }, tools: { image: false }, welcomeScreen: false }}><MainMenu /></Excalidraw></div>)}</div></> }
createRoot(document.getElementById('root')!).render(<App />)
declare global { interface Window { boardGate: typeof gate } }
