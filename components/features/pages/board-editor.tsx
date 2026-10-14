"use client"
import { useEffect, useMemo, useRef, useState } from "react"
import { boardImageSkeleton } from "@/lib/boards/image-element"
import { BoardLibrary } from "./board-library"
import { Excalidraw, convertToExcalidrawElements, MainMenu } from "@excalidraw/excalidraw"
import { CaptureUpdateAction } from "@excalidraw/excalidraw"
import type { AppState, ExcalidrawImperativeAPI, Collaborator, SocketId, BinaryFileData } from "@excalidraw/excalidraw/types"
import type { ExcalidrawElement } from "@excalidraw/excalidraw/element/types"
import "@excalidraw/excalidraw/index.css"
import "./board-editor.css"
import type { PageBoard } from "@/hooks/use-page-board"
import { BoardArtifactPlacement } from "./board-artifact-placement"
export type BoardEditorViewState = Pick<AppState, "zoom" | "scrollX" | "scrollY" | "activeTool" | "selectedElementIds">
const linkFor = (id: string) => `https://board-placement.invalid/${encodeURIComponent(id)}`
export const placementElementId = (id: string) => `board-placement:${id}`
export default function BoardEditor({ base, authorization, workspaceId, disabled, onChange, collaborators, onPointer, scene, onBusy, files, insertImage, onImageInserted, onSelection, focusPoint, onInteraction, onViewportChange, initialViewState, onViewState }: {
  initialViewState?: BoardEditorViewState
  onViewState?: (state: BoardEditorViewState) => void
  onViewportChange?: (scrollX: number, scrollY: number) => void
  onInteraction?: () => void
  focusPoint?: { x: number; y: number; requestId: string }
  onSelection?: (ids: string[]) => void
  files?: BinaryFileData[]
  insertImage?: import("@/lib/boards/files").BoardImageUpload | null
  onImageInserted?: () => void
  scene?: PageBoard
  onBusy?: (busy: boolean) => void
  base: PageBoard
  authorization: PageBoard
  workspaceId: string
  disabled: boolean
  collaborators: Map<SocketId, Collaborator>
  onPointer: (x: number, y: number) => void
  onChange: (elements: readonly ExcalidrawElement[]) => void
}) {
  const inserted = useRef(new Set<string>())
  const [api, setApi] = useState<ExcalidrawImperativeAPI | null>(null)
  useEffect(() => {
    if (!api || !focusPoint) return
    const state = api.getAppState()
    api.updateScene({ appState: { scrollX: state.width / (2 * state.zoom.value) - focusPoint.x, scrollY: state.height / (2 * state.zoom.value) - focusPoint.y }, captureUpdate: CaptureUpdateAction.NEVER })
  }, [api, focusPoint])
  useEffect(() => { if (api) api.updateScene({ collaborators, captureUpdate: CaptureUpdateAction.NEVER }) }, [api, collaborators])
  useEffect(() => { if (api && files?.length) api.addFiles(files) }, [api, files])
  useEffect(() => {
    if (!api || !insertImage || inserted.current.has(insertImage.file_id)) return
    // Let Excalidraw finish its initialData effect before inserting. An eager
    // update from the API-ready callback can be replaced by initialization.
    const frame = requestAnimationFrame(() => {
      if (inserted.current.has(insertImage.file_id)) return
      inserted.current.add(insertImage.file_id)
      const [image] = convertToExcalidrawElements([boardImageSkeleton(insertImage)], { regenerateIds: false })
      api.updateScene({ elements: [...api.getSceneElements(), image], captureUpdate: CaptureUpdateAction.IMMEDIATELY })
      onImageInserted?.()
    })
    return () => cancelAnimationFrame(frame)
  }, [api, insertImage, onImageInserted])
  const initial = useMemo(() => {
    const rendered = scene ?? base
    const drawings = rendered.records.filter(record => !record.deleted && record.element).map(record => record.element as ExcalidrawElement)
    const placements = convertToExcalidrawElements(rendered.placements.map(item => ({ type: "rectangle" as const, id: placementElementId(item.id), x: item.x, y: item.y, width: item.width, height: item.height })), { regenerateIds: false }).map(element => ({ ...element, type: "embeddable", link: linkFor(element.id) })) as ExcalidrawElement[]
    return [...drawings, ...placements]
  }, [base, scene])
  const safeLinks = useMemo(() => new Set(base.placements.map(item => linkFor(placementElementId(item.id)))), [base])
  return <div onClickCapture={event => {
    // 0.18.1 renders this built-in converter even with aiEnabled=false.
    // It must not process untrusted Mermaid in Studio. Paste is also disabled;
    // the product's separate-site converter is the only diagram input path.
    if (event.target instanceof Element && event.target.closest('[data-testid="toolbar-embeddable"]')) {
      event.preventDefault()
      event.stopPropagation()
    }
  }} onPointerDownCapture={onInteraction} onWheelCapture={onInteraction} onKeyDownCapture={event => { if (event.key === "Escape") onInteraction?.() }} className="crewship-board-editor h-[65dvh] min-h-96 w-full border-t" aria-label="Board drawing canvas"><Excalidraw
    excalidrawAPI={setApi} onScrollChange={onViewportChange} isCollaborating={collaborators.size > 0} onPointerUpdate={({ pointer }) => onPointer(pointer.x, pointer.y)}
    initialData={{ elements: initial, appState: initialViewState }} onChange={(elements, state) => { onViewState?.({ zoom: state.zoom, scrollX: state.scrollX, scrollY: state.scrollY, activeTool: state.activeTool, selectedElementIds: state.selectedElementIds }); onChange(elements); onSelection?.(Object.keys(state.selectedElementIds).filter(id => state.selectedElementIds[id])); onBusy?.(Boolean(state.editingTextElement || state.newElement || state.multiElement || state.isResizing || state.isRotating || state.selectedElementsAreBeingDragged)) }}
    viewModeEnabled={disabled} aiEnabled={false} handleKeyboardGlobally={false}
    onPaste={() => false} onLinkOpen={(_element, event) => event.preventDefault()}
    validateEmbeddable={link => safeLinks.has(link)}
    renderEmbeddable={element => {
      const placement = base.placements.find(item => placementElementId(item.id) === element.id)
      // Always return owned content, including for unknown embeds: never fall
      // through to Excalidraw's built-in arbitrary URL iframe renderer.
      return placement ? <BoardArtifactPlacement workspaceId={workspaceId} placement={authorization.placements.find(item => item.id === placement.id)} /> : <div className="p-4 text-sm">External embeds are unavailable.</div>
    }}
    UIOptions={{ canvasActions: { loadScene: false, saveToActiveFile: false, export: false, saveAsImage: false }, tools: { image: false }, welcomeScreen: false }}
  ><MainMenu /><BoardLibrary api={api} disabled={disabled} /></Excalidraw></div>
}
