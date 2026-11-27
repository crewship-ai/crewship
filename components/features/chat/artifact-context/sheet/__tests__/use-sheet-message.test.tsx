import { describe, it, expect, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { SHEET_EDIT_DEFAULT, useSheetMessage } from "../use-sheet-message"

type SheetMessageProps = Parameters<typeof useSheetMessage>[0]
const setup = (over: Partial<SheetMessageProps> = {}) =>
  renderHook((props: SheetMessageProps) => useSheetMessage(props), { initialProps: { scope: "u:ws:chat", pageId: "pg_1", blocked: null, version: "3", ...over } })

describe("the choice to let the agent change a Sheet", () => {
  beforeEach(() => sessionStorage.clear())
  // In the person's own private chat, a Sheet uploaded there lets the agent change it
  // without asking each time; the person can turn it off and it stays off.
  it("with the server's default on, lets the agent change the Sheet for every message, following each new version", () => {
    const { result, rerender } = setup({ defaultAllowed: true })
    expect(result.current.allowed).toBe(true)
    expect(result.current.metadata()).toEqual({ artifact_refs: [{ page_id: "pg_1", version: "3" }] })
    act(() => result.current.sent())
    expect(result.current.allowed, "not only for one message").toBe(true)
    rerender({ scope: "u:ws:chat", pageId: "pg_1", blocked: null, version: "4", ownSave: undefined, defaultAllowed: true })
    expect(result.current.metadata(), "the agent's own version does not ask again").toEqual({ artifact_refs: [{ page_id: "pg_1", version: "4" }] })
    expect(result.current.stale).toBe(false)
  })
  it("keeps it off once the person turned it off, for this Sheet in this chat, also after a reload", () => {
    const first = setup({ defaultAllowed: true })
    act(() => first.result.current.setAllowed(false))
    act(() => first.result.current.sent())
    expect(first.result.current.allowed).toBe(false)
    first.unmount()
    const again = setup({ defaultAllowed: true })
    expect(again.result.current.allowed).toBe(false)
    const other = setup({ defaultAllowed: true, pageId: "pg_2" })
    expect(other.result.current.allowed, "another Sheet starts from the server's default").toBe(true)
  })
  it("still sends nothing while blocked or without a version, whatever the default", () => {
    expect(setup({ defaultAllowed: true, blocked: "checking" }).result.current.metadata()).toBeUndefined()
    expect(setup({ defaultAllowed: true, version: undefined }).result.current.allowed).toBe(false)
  })
  it("is off by default and then sends nothing about the Sheet", () => {
    const { result } = setup()
    expect(SHEET_EDIT_DEFAULT).toBe(false)
    expect(result.current.allowed).toBe(false)
    expect(result.current.metadata()).toBeUndefined()
  })

  it("sends exactly the Sheet's id once it is chosen", () => {
    const { result } = setup()
    act(() => result.current.setAllowed(true))
    expect(result.current.metadata()).toEqual({ artifact_refs: [{ page_id: "pg_1", version: "3" }] })
  })

  it("is for one message: it goes back off after a send", () => {
    const { result } = setup()
    act(() => result.current.setAllowed(true))
    act(() => result.current.sent())
    expect(result.current.allowed).toBe(false)
    expect(result.current.metadata()).toBeUndefined()
  })

  it("never carries over to another Sheet or another conversation", () => {
    const { result, rerender } = setup()
    act(() => result.current.setAllowed(true))
    rerender({ scope: "u:ws:chat", pageId: "pg_2", blocked: null, version: "3", ownSave: undefined })
    expect(result.current.metadata()).toBeUndefined()
    rerender({ scope: "u:ws:other", pageId: "pg_1", blocked: null, version: "3", ownSave: undefined })
    expect(result.current.metadata()).toBeUndefined()
    rerender({ scope: "u:ws:chat", pageId: "pg_1", blocked: null, version: "3", ownSave: undefined })
    expect(result.current.metadata()).toBeUndefined()
  })

  it("sends nothing while it cannot apply, even if it was on", () => {
    const { result, rerender } = setup()
    act(() => result.current.setAllowed(true))
    rerender({ scope: "u:ws:chat", pageId: "pg_1", blocked: "A message carries either a Page or a Sheet.", version: "3", ownSave: undefined })
    expect(result.current.metadata()).toBeUndefined()
  })

  it("binds the choice to the version the person saw when choosing", () => {
    const { result, rerender } = setup()
    act(() => result.current.setAllowed(true))
    rerender({ scope: "u:ws:chat", pageId: "pg_1", blocked: null, version: "3", ownSave: undefined })
    expect(result.current.metadata()?.artifact_refs[0].version).toBe("3")
  })

  it("follows the person's own save while the choice is on: they saw that version", () => {
    const { result, rerender } = setup()
    act(() => result.current.setAllowed(true))
    rerender({ scope: "u:ws:chat", pageId: "pg_1", blocked: null, version: "4", ownSave: "4" })
    expect(result.current.allowed).toBe(true)
    expect(result.current.stale).toBe(false)
    expect(result.current.metadata()).toEqual({ artifact_refs: [{ page_id: "pg_1", version: "4" }] })
  })

  it("turns the choice off and asks again when someone else changed the Sheet", () => {
    const { result, rerender } = setup()
    act(() => result.current.setAllowed(true))
    rerender({ scope: "u:ws:chat", pageId: "pg_1", blocked: null, version: "4", ownSave: undefined })
    expect(result.current.allowed).toBe(false)
    expect(result.current.stale).toBe(true)
    expect(result.current.metadata()).toBeUndefined()
    act(() => result.current.setAllowed(true))
    expect(result.current.stale).toBe(false)
    expect(result.current.metadata()).toEqual({ artifact_refs: [{ page_id: "pg_1", version: "4" }] })
  })

  it("cannot be chosen before the version shown is known", () => {
    const { result } = setup({ version: undefined })
    act(() => result.current.setAllowed(true))
    expect(result.current.metadata()).toBeUndefined()
  })
})
