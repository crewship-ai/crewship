import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { NewArtifactMenu, NewArtifactPanel } from "../new-artifact-menu"
import { availableArtifactKinds } from "../new-artifact-kinds"

afterEach(cleanup)

it("lists only kinds the backend serves, with the prototype's copy", () => {
  const all = availableArtifactKinds({ projects: true, documents: true })
  expect(all.map(k => [k.id, k.name, k.description])).toEqual([
    ["project", "Project", "Screens that link together"],
    ["word", "Word", "A document, one editor at a time"],
  ])
  expect(all[0].starterPrompts).toEqual(["A three-screen pricing site: plans, compare, FAQ", "A sign-up flow with a confirmation screen"])
  expect(all[1].starterPrompts).toEqual(["A partner brief from this chat", "Turn the weekly report into a two-page memo"])
  expect(availableArtifactKinds({ projects: false, documents: true }).map(k => k.id)).toEqual(["word"])
  expect(availableArtifactKinds({ projects: true, documents: false }).map(k => k.id)).toEqual(["project"])
  expect(availableArtifactKinds({ projects: false, documents: false })).toEqual([])
})

it("renders nothing when no kind is available", () => {
  const { container } = render(<NewArtifactMenu kinds={[]} onPick={() => {}} />)
  expect(container.innerHTML).toBe("")
  const panel = render(<NewArtifactPanel kinds={[]} onPick={() => {}} />)
  expect(panel.container.innerHTML).toBe("")
})

it("shows only the available kinds and never planned ones", () => {
  render(<NewArtifactMenu kinds={availableArtifactKinds({ projects: false, documents: true })} onPick={() => {}} />)
  fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
  expect(screen.getByRole("button", { name: /Word/ })).toBeTruthy()
  expect(screen.queryByRole("button", { name: /Project/ })).toBeNull()
  for (const missing of [/Dashboard/, /Slides/, /Sheet/, /Docs/, /Design/, /Planned/]) expect(screen.queryByText(missing)).toBeNull()
})

it("selects the first kind, shows its starters, and switches on tile click", () => {
  const kinds = availableArtifactKinds({ projects: true, documents: true })
  render(<NewArtifactPanel kinds={kinds} onPick={() => {}} />)
  expect(screen.getByRole("button", { name: /Project/ }).getAttribute("aria-pressed")).toBe("true")
  expect(screen.getByRole("button", { name: kinds[0].starterPrompts[0] })).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: /Word/ }))
  expect(screen.getByRole("button", { name: /Word/ }).getAttribute("aria-pressed")).toBe("true")
  expect(screen.getByRole("button", { name: /Project/ }).getAttribute("aria-pressed")).toBe("false")
  expect(screen.getByRole("button", { name: kinds[1].starterPrompts[1] })).toBeTruthy()
  expect(screen.queryByRole("button", { name: kinds[0].starterPrompts[0] })).toBeNull()
})

it("picking a starter calls onPick and closes the menu", () => {
  const onPick = vi.fn()
  const kinds = availableArtifactKinds({ projects: true, documents: true })
  render(<NewArtifactMenu kinds={kinds} onPick={onPick} />)
  fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
  fireEvent.click(screen.getByRole("button", { name: /Word/ }))
  fireEvent.click(screen.getByRole("button", { name: kinds[1].starterPrompts[0] }))
  expect(onPick).toHaveBeenCalledWith("word", kinds[1].starterPrompts[0])
  expect(screen.queryByRole("dialog", { name: "New artifact" })).toBeNull()
})

it("closes on Escape and the close button, and respects disabled", () => {
  const kinds = availableArtifactKinds({ projects: true, documents: true })
  const { rerender } = render(<NewArtifactMenu kinds={kinds} onPick={() => {}} />)
  fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
  expect(screen.getByRole("dialog", { name: "New artifact" })).toBeTruthy()
  fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
  expect(screen.queryByRole("dialog", { name: "New artifact" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
  fireEvent.click(screen.getByRole("button", { name: "Close" }))
  expect(screen.queryByRole("dialog", { name: "New artifact" })).toBeNull()
  rerender(<NewArtifactMenu kinds={kinds} onPick={() => {}} disabled />)
  expect((screen.getByRole("button", { name: "New artifact" }) as HTMLButtonElement).disabled).toBe(true)
})
