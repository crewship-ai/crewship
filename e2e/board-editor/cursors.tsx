import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { convertToExcalidrawElements } from '@excalidraw/excalidraw'
import type { Collaborator, SocketId } from '@excalidraw/excalidraw/types'
import type { ExcalidrawElement } from '@excalidraw/excalidraw/element/types'
import { useBoardFollow } from '../../hooks/use-board-follow'
import { BoardFollowControls } from '../../components/features/pages/board-follow-controls'
import BoardEditor from '../../components/features/pages/board-editor'
import type { PageBoard } from '../../hooks/use-page-board'
const elements = convertToExcalidrawElements([{ type: 'rectangle', id: 'shape', x: 30, y: 30, width: 80, height: 80 }], { regenerateIds: false })
const board: PageBoard = { format: 'board/v1', seq: 7, can_write: true, placements: [], records: elements.map(element => ({ element_id: element.id, element_rev: 1, element, deleted: false })) }
let viewport: { x: number; y: number } | null = null
let observed: readonly ExcalidrawElement[] = []
function App() {
  const [collaborators, setCollaborators] = useState(new Map<SocketId, Collaborator>())
  const [point, setPoint] = useState<{ x: number; y: number } | null>(null)
  const [available, setAvailable] = useState(true)
  const follow = useBoardFollow(point && available ? [{ connection_id: 'verified-peer', user_id: 'human-peer', revision: null, board_seq: 7, cursor: { ...point, coordinate_space: 'board' } }] : [], 'fixture-self', available, 7, true)
  const show = (x = 200, y = 180) => { setPoint({ x, y }); setCollaborators(new Map<SocketId, Collaborator>([['verified-peer' as SocketId, { id: 'human-peer', username: 'Fixture teammate', pointer: { x, y, tool: 'pointer' } }]])) }
  Object.assign(window, { boardCursors: { ready: () => observed.length > 0, scene: () => structuredClone(observed), viewport: () => viewport, show, move: (x: number, y: number) => show(x,y), disconnect: () => { setAvailable(false); setCollaborators(new Map()); setPoint(null) } } })
  return <div onKeyDownCapture={event => { if (event.key === 'Escape') follow.stop() }}><BoardFollowControls follow={follow} names={{ 'human-peer': 'Fixture teammate' }} onSelect={follow.select} /><BoardEditor focusPoint={follow.focusPoint} onInteraction={follow.stop} onViewportChange={(x,y) => { viewport = { x,y } }} base={board} authorization={board} workspaceId="fixture" disabled={false} onChange={scene => { observed = scene }} collaborators={collaborators} onPointer={() => {}} /></div>
}
createRoot(document.getElementById('root')!).render(<App />)
declare global { interface Window { boardCursors: { ready: () => boolean; scene: () => readonly ExcalidrawElement[]; show: () => void; move: (x: number,y: number) => void; viewport: () => {x:number;y:number}|null; disconnect: () => void } } }
