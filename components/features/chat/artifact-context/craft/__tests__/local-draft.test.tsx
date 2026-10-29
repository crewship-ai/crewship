import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useLayoutEffect } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))

import { discardLocalDraft, readLocalDraft, saveLocalDraft, type LocalDraft } from "../local-draft"
import { LocalDraftBanner } from "../local-draft-banner"

const draft: LocalDraft = { content: "Hello draft", baseVersion: 3, savedAt: new Date(Date.now() - 5 * 60_000).toISOString() }
const fetchSpy = vi.fn()

// vitest.setup.ts replaces localStorage with inert vi.fn stubs; back them with a real map here.
const store = new Map<string, string>()
beforeEach(() => {
  store.clear()
  vi.mocked(localStorage.getItem).mockImplementation((k: string) => store.get(k) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((k: string, v: string) => { store.set(k, String(v)) })
  vi.mocked(localStorage.removeItem).mockImplementation((k: string) => { store.delete(k) })
  vi.stubGlobal("fetch", fetchSpy)
})
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); apiFetch.mockReset(); fetchSpy.mockReset() })

it("round-trips a draft keyed by user, workspace and slug", () => {
  saveLocalDraft("u1", "w1", "doc", draft)
  expect(readLocalDraft("u1", "w1", "doc")).toEqual(draft)
  expect(readLocalDraft("u2", "w1", "doc")).toBeNull()
  expect(readLocalDraft("u1", "w2", "doc")).toBeNull()
  expect(readLocalDraft("u1", "w1", "other")).toBeNull()
  discardLocalDraft("u1", "w1", "doc")
  expect(readLocalDraft("u1", "w1", "doc")).toBeNull()
})

it("ignores malformed stored values", () => {
  saveLocalDraft("u1", "w1", "doc", draft)
  const key = [...store.keys()].find(k => store.get(k)?.includes("Hello draft"))!
  store.set(key, "{not json")
  expect(readLocalDraft("u1", "w1", "doc")).toBeNull()
  store.set(key, JSON.stringify({ content: 1 }))
  expect(readLocalDraft("u1", "w1", "doc")).toBeNull()
})

it("is safe when storage throws (private mode)", () => {
  vi.mocked(localStorage.setItem).mockImplementation(() => { throw new Error("QuotaExceeded") })
  vi.mocked(localStorage.getItem).mockImplementation(() => { throw new Error("SecurityError") })
  vi.mocked(localStorage.removeItem).mockImplementation(() => { throw new Error("SecurityError") })
  expect(saveLocalDraft("u1", "w1", "doc", draft)).toBe(false)
  expect(readLocalDraft("u1", "w1", "doc")).toBeNull()
  expect(() => discardLocalDraft("u1", "w1", "doc")).not.toThrow()
  const { container } = render(<LocalDraftBanner userId="u1" workspaceId="w1" slug="doc" onCompare={() => {}} />)
  expect(container.textContent).toBe("")
})

it("offers a restored draft for compare, copy or discard and never saves it to the server", async () => {
  saveLocalDraft("u1", "w1", "doc", draft)
  const writeText = vi.fn().mockResolvedValue(undefined)
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } })
  const onCompare = vi.fn()
  render(<LocalDraftBanner userId="u1" workspaceId="w1" slug="doc" onCompare={onCompare} />)
  expect(await screen.findByText("Recovered local draft")).toBeTruthy()
  expect(screen.getByText(/^From .+, before your browser closed\. Not saved yet\.$/)).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Compare" }))
  expect(onCompare).toHaveBeenCalledWith(draft)
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Copy text" })) })
  expect(writeText).toHaveBeenCalledWith("Hello draft")
  expect(await screen.findByText("Copied")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Discard draft" }))
  await waitFor(() => expect(screen.queryByText("Recovered local draft")).toBeNull())
  expect(readLocalDraft("u1", "w1", "doc")).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
  expect(fetchSpy).not.toHaveBeenCalled()
})

it("says the draft is kept off another holder's work", async () => {
  saveLocalDraft("u1", "w1", "doc", draft)
  render(<LocalDraftBanner userId="u1" workspaceId="w1" slug="doc" holderName="Jana" onCompare={() => {}} />)
  expect(await screen.findByText(/^From .+, before your browser closed\. Jana edits now, so it is not saved over their work\.$/)).toBeTruthy()
})

it("renders nothing without a draft", () => {
  const { container } = render(<LocalDraftBanner userId="u1" workspaceId="w1" slug="doc" onCompare={() => {}} />)
  expect(container.textContent).toBe("")
})

it("never renders the previous person's draft after the identity changes, not even for one commit", () => {
  saveLocalDraft("u1", "w1", "doc", { ...draft, content: "ALICE_PRIVATE_SECRET" })
  const commits: string[] = []
  // Layout effects run right after the DOM commit and before passive effects:
  // a reset done only in useEffect would still be on screen here.
  function Probe() { useLayoutEffect(() => { commits.push(document.body.textContent ?? "") }); return null }
  const banner = (userId: string) => <><LocalDraftBanner userId={userId} workspaceId="w1" slug="doc" holderName={null} onCompare={vi.fn()} /><Probe /></>
  const { rerender } = render(banner("u1"))
  expect(screen.getByText("Recovered local draft")).toBeTruthy()
  commits.length = 0
  rerender(banner("u2"))
  expect(commits.length).toBeGreaterThan(0)
  expect(commits.filter(text => text.includes("Recovered local draft"))).toEqual([])
  expect(screen.queryByRole("button", { name: "Compare" })).toBeNull()
})
