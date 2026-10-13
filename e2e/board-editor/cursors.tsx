import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { convertToExcalidrawElements } from '@excalidraw/excalidraw'
import type { Collaborator, SocketId } from '@excalidraw/excalidraw/types'
import type { ExcalidrawElement } from '@excalidraw/excalidraw/element/types'
import BoardEditor from '../../components/features/pages/board-editor'
import type { PageBoard } from '../../hooks/use-page-board'
const elements = convertToExcalidrawElements([{ type: 'rectangle', id: 'shape', x: 30, y: 30, width: 80, height: 80 }], { regenerateIds: false })
const board: PageBoard = { format: 'board/v1', seq: 7, can_write: true, placements: [], records: elements.map(element => ({ element_id: element.id, element_rev: 1, element, deleted: false })) }
let observed: readonly ExcalidrawElement[] = []
function App() {
  const [collaborators, setCollaborators] = useState(new Map<SocketId, Collaborator>())
  Object.assign(window, { boardCursors: { ready: () => observed.length > 0, scene: () => structuredClone(observed), show: () => setCollaborators(new Map<SocketId, Collaborator>([['verified-peer' as SocketId, { id: 'human-peer', username: 'Fixture teammate', pointer: { x: 200, y: 180, tool: 'pointer' } }]])) } })
  return <BoardEditor base={board} authorization={board} workspaceId="fixture" disabled={false} onChange={scene => { observed = scene }} collaborators={collaborators} onPointer={() => {}} />
}
createRoot(document.getElementById('root')!).render(<App />)
declare global { interface Window { boardCursors: { ready: () => boolean; scene: () => readonly ExcalidrawElement[]; show: () => void } } }
