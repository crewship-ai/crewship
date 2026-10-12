import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { useLocalAttachments, useProviderAttachmentsState } from "../prompt-input/hooks/use-attachments"

const file = (name = "note.txt", type = "text/plain", content = "hello") => new File([content], name, { type })
let serial = 0
beforeEach(() => {
  serial = 0
  vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:test-${++serial}`)
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {})
})
afterEach(() => { vi.restoreAllMocks() })

describe("provider attachments", () => {
  it("appends files with unique IDs and metadata, and ignores empty selections", () => {
    const { result, unmount } = renderHook(() => useProviderAttachmentsState())
    act(() => result.current.attachments.add([]))
    expect(URL.createObjectURL).not.toHaveBeenCalled()
    act(() => result.current.attachments.add([file(), file("picture.png", "image/png")]))
    expect(result.current.attachments.files).toMatchObject([
      { filename: "note.txt", mediaType: "text/plain", type: "file", url: "blob:test-1" },
      { filename: "picture.png", mediaType: "image/png", type: "file", url: "blob:test-2" },
    ])
    expect(new Set(result.current.attachments.files.map(f => f.id)).size).toBe(2)
    act(() => result.current.attachments.remove("missing"))
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    act(() => result.current.attachments.remove(result.current.attachments.files[0].id))
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:test-1")
    expect(result.current.attachments.files).toHaveLength(1)
    act(() => result.current.attachments.clear())
    expect(result.current.attachments.files).toEqual([])
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:test-2")
    unmount()
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(2)
  })
  it("releases the latest list on unmount", () => {
    const { result, unmount } = renderHook(() => useProviderAttachmentsState())
    act(() => result.current.attachments.add([file()]))
    act(() => result.current.attachments.add([file()]))
    unmount()
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(2)
  })
  it("opens the registered input and tolerates opening before registration", () => {
    const { result } = renderHook(() => useProviderAttachmentsState())
    act(() => result.current.attachments.openFileDialog())
    const input = document.createElement("input")
    const open = vi.fn()
    act(() => result.current.__registerFileInput({ current: input }, open))
    act(() => result.current.attachments.openFileDialog())
    expect(open).toHaveBeenCalledOnce()
    expect(result.current.attachments.fileInputRef.current).toBe(input)
  })
})

describe("local attachments", () => {
  it.each([undefined, "", "  ", "text/plain", "image/*, text/plain, "])("accepts matching input for %s", accept => {
    const { result } = renderHook(() => useLocalAttachments({ accept }))
    act(() => result.current.addLocal([file()]))
    expect(result.current.items).toMatchObject([{ filename: "note.txt", mediaType: "text/plain", type: "file", url: "blob:test-1" }])
  })
  it("filters nonmatching types while allowing wildcard matches", () => {
    const onError = vi.fn()
    const { result } = renderHook(() => useLocalAttachments({ accept: "image/*", onError }))
    act(() => result.current.addLocal([file(), file("photo.png", "image/png")]))
    expect(result.current.items.map(f => f.filename)).toEqual(["photo.png"])
    expect(onError).not.toHaveBeenCalled()
    act(() => result.current.addLocal([file()]))
    expect(onError).toHaveBeenCalledWith({ code: "accept", message: "No files match the accepted types." })
  })
  it("enforces the size boundary without discarding valid files", () => {
    const onError = vi.fn()
    const { result } = renderHook(() => useLocalAttachments({ maxFileSize: 5, onError }))
    act(() => result.current.addLocal([file(), file("big.txt", "text/plain", "123456")]))
    expect(result.current.items.map(f => f.filename)).toEqual(["note.txt"])
    expect(onError).not.toHaveBeenCalled()
    act(() => result.current.addLocal([file("big.txt", "text/plain", "123456")]))
    expect(onError).toHaveBeenCalledWith({ code: "max_file_size", message: "All files exceed the maximum size." })
  })
  it("caps additions against existing items and frees capacity after removal", () => {
    const onError = vi.fn()
    const { result } = renderHook(() => useLocalAttachments({ maxFiles: 2, onError }))
    act(() => result.current.addLocal([file()]))
    act(() => result.current.addLocal([file("second"), file("third")]))
    expect(result.current.items.map(f => f.filename)).toEqual(["note.txt", "second"])
    expect(onError).toHaveBeenCalledWith({ code: "max_files", message: "Too many files. Some were not added." })
    act(() => result.current.removeLocal("missing"))
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    act(() => result.current.removeLocal(result.current.items[0].id))
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:test-1")
    act(() => result.current.addLocal([file("third")]))
    expect(result.current.items.map(f => f.filename)).toEqual(["second", "third"])
    act(() => result.current.clearLocal())
    expect(result.current.items).toEqual([])
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(3)
  })
  it.each([{ accept: "image/*" }, { maxFileSize: 1 }, { maxFiles: 0 }])("handles rejection without an error callback: %j", options => {
    const { result } = renderHook(() => useLocalAttachments(options))
    act(() => result.current.addLocal([file()]))
    expect(result.current.items).toEqual([])
  })
  it("does not report errors for empty selections", () => {
    const onError = vi.fn()
    const { result } = renderHook(() => useLocalAttachments({ maxFileSize: 1, onError }))
    act(() => result.current.addLocal([]))
    expect(onError).not.toHaveBeenCalled()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })
  it("opens the local file input if mounted", () => {
    const { result } = renderHook(() => useLocalAttachments({}))
    act(() => result.current.openFileDialogLocal())
    const input = document.createElement("input")
    const click = vi.spyOn(input, "click")
    result.current.inputRef.current = input
    act(() => result.current.openFileDialogLocal())
    expect(click).toHaveBeenCalledOnce()
  })
})
