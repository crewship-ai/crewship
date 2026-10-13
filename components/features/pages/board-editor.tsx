"use client"
import { useMemo } from "react"
import { Excalidraw, convertToExcalidrawElements, MainMenu } from "@excalidraw/excalidraw"
import type { ExcalidrawElement } from "@excalidraw/excalidraw/element/types"
import "@excalidraw/excalidraw/index.css"
import type { PageBoard } from "@/hooks/use-page-board"
import { BoardArtifactPlacement } from "./board-artifact-placement"
const linkFor = (id: string) => `https://board-placement.invalid/${encodeURIComponent(id)}`
export const placementElementId = (id: string) => `board-placement:${id}`
export default function BoardEditor({ base, authorization, workspaceId, disabled, onChange }: {
  base: PageBoard
  authorization: PageBoard
  workspaceId: string
  disabled: boolean
  onChange: (elements: readonly ExcalidrawElement[]) => void
}) {
  const initial = useMemo(() => {
    const drawings = base.records.filter(record => !record.deleted && record.element).map(record => record.element as ExcalidrawElement)
    const placements = convertToExcalidrawElements(base.placements.map(item => ({ type: "rectangle" as const, id: placementElementId(item.id), x: item.x, y: item.y, width: item.width, height: item.height })), { regenerateIds: false }).map(element => ({ ...element, type: "embeddable", link: linkFor(element.id) })) as ExcalidrawElement[]
    return [...drawings, ...placements]
  }, [base])
  const safeLinks = useMemo(() => new Set(base.placements.map(item => linkFor(placementElementId(item.id)))), [base])
  return <div className="h-[65dvh] min-h-96 w-full border-t" aria-label="Board drawing canvas"><Excalidraw
    initialData={{ elements: initial }} onChange={elements => onChange(elements)}
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
  ><MainMenu /></Excalidraw></div>
}
