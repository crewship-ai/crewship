import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

import { useDirtyForm } from "@/hooks/use-dirty-form"

describe("useDirtyForm", () => {
  beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }) })
  afterEach(() => { vi.useRealTimers() })

  it("starts clean and mirrors the baseline", () => {
    const { result } = renderHook(() => useDirtyForm({ name: "Ops", cpus: 2 }))
    expect(result.current.isDirty).toBe(false)
    expect(result.current.draft).toEqual({ name: "Ops", cpus: 2 })
  })

  it("goes dirty on a real change and clean again when the value is typed back", () => {
    const { result } = renderHook(() => useDirtyForm({ name: "Ops" }))

    act(() => result.current.set("name", "Ops rebrand"))
    expect(result.current.isDirty).toBe(true)

    // Typing the original value back is NOT a change — a Save button that
    // stays armed here trains people to ignore it.
    act(() => result.current.set("name", "Ops"))
    expect(result.current.isDirty).toBe(false)
  })

  it("reset() throws the draft away and goes clean", () => {
    const { result } = renderHook(() => useDirtyForm({ name: "Ops" }))
    act(() => result.current.set("name", "typo"))
    act(() => result.current.reset())
    expect(result.current.draft.name).toBe("Ops")
    expect(result.current.isDirty).toBe(false)
  })

  // The baseline is server data and can refetch under the user mid-edit.
  it("follows a new baseline while clean", () => {
    const { result, rerender } = renderHook(({ b }) => useDirtyForm(b), {
      initialProps: { b: { name: "Ops" } },
    })
    rerender({ b: { name: "Ops renamed elsewhere" } })
    expect(result.current.draft.name).toBe("Ops renamed elsewhere")
    expect(result.current.isDirty).toBe(false)
  })

  it("does NOT clobber the draft when a refetch lands mid-edit", () => {
    const { result, rerender } = renderHook(({ b }) => useDirtyForm(b), {
      initialProps: { b: { name: "Ops" } },
    })
    act(() => result.current.set("name", "half-typed n"))
    rerender({ b: { name: "Ops renamed elsewhere" } })
    // Losing someone's half-typed text to a background poll is the worst
    // possible outcome here, so the draft wins until they save or cancel.
    expect(result.current.draft.name).toBe("half-typed n")
    expect(result.current.isDirty).toBe(true)
  })

  it("submit() runs saving -> saved -> idle and rebases the baseline", async () => {
    const save = vi.fn().mockResolvedValue(undefined)
    const { result } = renderHook(() => useDirtyForm({ name: "Ops" }, { savedMs: 2000 }))

    act(() => result.current.set("name", "Ops rebrand"))
    let pending!: Promise<void>
    act(() => { pending = result.current.submit(save) })
    expect(result.current.status).toBe("saving")

    await act(async () => { await pending })
    expect(save).toHaveBeenCalledWith({ name: "Ops rebrand" })
    expect(result.current.status).toBe("saved")
    // Rebased: the saved value is the new clean state, so the footer collapses
    // instead of insisting there is still something to save.
    expect(result.current.isDirty).toBe(false)

    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(result.current.status).toBe("idle")
  })

  it("keeps the draft and reports the message when submit() fails", async () => {
    const save = vi.fn().mockRejectedValue(new Error("workspace name already taken"))
    const { result } = renderHook(() => useDirtyForm({ name: "Ops" }))

    act(() => result.current.set("name", "Duplicate"))
    await act(async () => { await result.current.submit(save) })

    expect(result.current.status).toBe("error")
    expect(result.current.error).toBe("workspace name already taken")
    // Never silently revert what the user typed on a failed save.
    expect(result.current.draft.name).toBe("Duplicate")
    expect(result.current.isDirty).toBe(true)
  })

  it("ignores a submit() while one is already in flight", async () => {
    const save = vi.fn().mockResolvedValue(undefined)
    const { result } = renderHook(() => useDirtyForm({ name: "Ops" }))
    act(() => result.current.set("name", "x"))

    let a!: Promise<void>, b!: Promise<void>
    act(() => { a = result.current.submit(save); b = result.current.submit(save) })
    await act(async () => { await Promise.all([a, b]) })

    // Double-clicking Save must not fire two writes.
    expect(save).toHaveBeenCalledTimes(1)
  })

  it("patch() applies several fields at once", () => {
    const { result } = renderHook(() => useDirtyForm({ name: "Ops", cpus: 2 }))
    act(() => result.current.patch({ name: "Ops2", cpus: 4 }))
    expect(result.current.draft).toEqual({ name: "Ops2", cpus: 4 })
    expect(result.current.isDirty).toBe(true)
  })

  it("clears a stale error once the user edits again", async () => {
    const save = vi.fn().mockRejectedValue(new Error("nope"))
    const { result } = renderHook(() => useDirtyForm({ name: "Ops" }))
    act(() => result.current.set("name", "bad"))
    await act(async () => { await result.current.submit(save) })
    expect(result.current.status).toBe("error")

    act(() => result.current.set("name", "better"))
    // A red footer next to a field the user has since fixed is just noise.
    expect(result.current.status).toBe("idle")
    expect(result.current.error).toBeNull()
  })

  it("does not update state after unmount", async () => {
    let release!: () => void
    const save = vi.fn(() => new Promise<void>((r) => { release = r }))
    const { result, unmount } = renderHook(() => useDirtyForm({ name: "Ops" }))
    act(() => result.current.set("name", "x"))

    let pending!: Promise<void>
    act(() => { pending = result.current.submit(save) })
    unmount()
    await act(async () => { release(); await pending })
    // No "state update on unmounted component" warning — nothing to assert
    // beyond the absence of a throw, which waitFor below gives a tick for.
    await waitFor(() => expect(save).toHaveBeenCalled())
  })
})
