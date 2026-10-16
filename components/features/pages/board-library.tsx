"use client"
import { useEffect, useRef } from "react"
import { CaptureUpdateAction, convertToExcalidrawElements, DefaultSidebar, Sidebar } from "@excalidraw/excalidraw"
import type { ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types"
import { BOARD_LIBRARY } from "@/lib/boards/library"

/** Route library controls to an owned sidebar via public APIs. The upstream
 * DefaultSidebar extends its built-in library, so it is not our renderer. */
export function BoardLibrary({ api, disabled }: { api: ExcalidrawImperativeAPI | null; disabled: boolean }) {
  const marker = useRef<HTMLSpanElement>(null)
  useEffect(() => {
    if (!api) return
    // Library and search keyboard paths can request the built-in sidebar too.
    // Always redirect those UI requests; they never import a library file.
    return api.onChange((_elements, state) => {
      if (state.openSidebar?.name === "default") api.toggleSidebar({ name: "crewship-library", force: true })
    })
  }, [api])
  useEffect(() => {
    const editor = marker.current?.closest(".excalidraw")
    if (!editor || !api) return
    const key = (event: Event) => {
      const keyboard = event as KeyboardEvent
      if (keyboard.key !== "0" || keyboard.ctrlKey || keyboard.metaKey || keyboard.altKey || keyboard.isComposing) return
      if (keyboard.target instanceof Element && keyboard.target.closest("input,textarea,select,[contenteditable=true]")) return
      keyboard.preventDefault(); keyboard.stopPropagation()
      api.toggleSidebar({ name: "crewship-library" })
    }
    editor.addEventListener("keydown", key, true)
    return () => editor.removeEventListener("keydown", key, true)
  }, [api])
  return <><span ref={marker} hidden />
    <DefaultSidebar.Trigger title="Crewship library" onToggle={open => { if (typeof open === "boolean") api?.toggleSidebar({ name: "crewship-library" }) }} />
    <Sidebar name="crewship-library">
    <Sidebar.Header><strong className="text-sm">Crewship library</strong></Sidebar.Header>
    <div className="space-y-3 p-3">
      <p className="text-xs text-muted-foreground">Reusable drawings for diagrams and wireframes. Insert, then customize.</p>
      <div className="grid grid-cols-2 gap-2">{BOARD_LIBRARY.map(item => <button key={item.id} type="button" disabled={disabled || !api} className="rounded-md border px-3 py-3 text-sm hover:bg-muted disabled:opacity-50 coarse:min-h-12" onClick={() => {
        if (!api || disabled) return
        const state = api.getAppState()
        const x = state.width / (2 * state.zoom.value) - state.scrollX - 80
        const y = state.height / (2 * state.zoom.value) - state.scrollY - 60
        const group = crypto.randomUUID()
        const elements = convertToExcalidrawElements(item.elements.map(element => ({ ...element, x: element.x + x, y: element.y + y, groupIds: [group], roughness: 0, strokeWidth: 2 })))
        api.updateScene({ elements: [...api.getSceneElementsIncludingDeleted(), ...elements], appState: { selectedElementIds: Object.fromEntries(elements.map(element => [element.id, true])), selectedGroupIds: { [group]: true } }, captureUpdate: CaptureUpdateAction.IMMEDIATELY })
      }}>{item.name}</button>)}</div>
      <p className="text-xs text-muted-foreground">Original Crewship drawings · Apache-2.0</p>
    </div>
  </Sidebar></>
}
