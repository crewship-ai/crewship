import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

const available = vi.hoisted(() => ({ value: true }))
vi.mock("../use-artifact-project-data", () => ({ useArtifactProjectsAvailable: () => available.value }))

import { ArtifactViewerProvider, useArtifactToolCardsEnabled, useArtifactViewer } from "../artifact-viewer-context"

afterEach(() => { cleanup(); available.value = true })
const wrapper = ({ children }: { children: ReactNode }) => <ArtifactViewerProvider workspaceId="ws-1">{children}</ArtifactViewerProvider>

it("is null outside the provider, so the feature is off", () => {
  expect(renderHook(() => useArtifactViewer()).result.current).toBeNull()
  expect(renderHook(() => useArtifactToolCardsEnabled()).result.current).toBe(false)
})

it("open replaces the target and close clears it", () => {
  const { result } = renderHook(() => useArtifactViewer()!, { wrapper })
  expect(result.current.workspaceId).toBe("ws-1")
  expect(result.current.target).toBeNull()
  act(() => result.current.open({ slug: "a" }))
  act(() => result.current.open({ slug: "b", tab: "comments" }))
  expect(result.current.target).toEqual({ slug: "b", tab: "comments" })
  act(() => result.current.close())
  expect(result.current.target).toBeNull()
})

it("cards are enabled only when the server reports projects available", () => {
  expect(renderHook(() => useArtifactToolCardsEnabled(), { wrapper }).result.current).toBe(true)
  available.value = false
  expect(renderHook(() => useArtifactToolCardsEnabled(), { wrapper }).result.current).toBe(false)
})

function Harness() {
  const viewer = useArtifactViewer()!
  return <>
    <button onClick={() => viewer.open({ slug: "a" })}>opener</button>
    {viewer.target && <div><button autoFocus onClick={() => viewer.open({ slug: "a", tab: "versions" })}>inner</button>
      <button onClick={() => viewer.close()}>close</button></div>}
  </>
}

it("close returns focus to the element that first opened the viewer", () => {
  render(<Harness />, { wrapper })
  const opener = screen.getByText("opener")
  opener.focus()
  fireEvent.click(opener)
  // Switching tab from inside the viewer must not move the return target into the viewer.
  screen.getByText("inner").focus()
  fireEvent.click(screen.getByText("inner"))
  screen.getByText("close").focus()
  fireEvent.click(screen.getByText("close"))
  expect(document.activeElement).toBe(opener)
})

it("close does not throw when the opener is gone", () => {
  const { result } = renderHook(() => useArtifactViewer()!, { wrapper })
  const gone = document.createElement("button")
  document.body.appendChild(gone)
  gone.focus()
  act(() => result.current.open({ slug: "a" }))
  gone.remove()
  expect(() => act(() => result.current.close())).not.toThrow()
})
