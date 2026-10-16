import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import type { Editor, Range } from "@tiptap/core"

import { TiptapEditor } from "../tiptap-editor"
import type { SlashCommandItem } from "../tiptap-editor-slash"

/**
 * The issue editor's three URL inputs — link, toolbar image, slash image —
 * collect a URL through a kit dialog, not the browser's prompt (#3343).
 *
 * These drive the real Tiptap editor: the point is what lands in the
 * DOCUMENT, so a mocked editor would only prove the dialog renders.
 */

let promptSpy: ReturnType<typeof vi.fn>

beforeEach(() => {
  promptSpy = vi.fn(() => null)
  vi.stubGlobal("prompt", promptSpy)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

async function renderEditor(content: string) {
  const onChange = vi.fn()
  render(<TiptapEditor content={content} onChange={onChange} />)
  const dom = await waitFor(() => {
    const el = document.querySelector(".ProseMirror") as (HTMLElement & { editor?: Editor }) | null
    if (!el?.editor) throw new Error("editor not mounted")
    return el
  })
  return { editor: dom.editor as Editor, dom, onChange }
}

function dialog() {
  return screen.getByRole("dialog")
}

function urlInput() {
  return within(dialog()).getByLabelText(/url/i) as HTMLInputElement
}

function typeUrl(value: string) {
  fireEvent.change(urlInput(), { target: { value } })
}

async function pressEnter() {
  await act(async () => {
    fireEvent.keyDown(urlInput(), { key: "Enter" })
  })
}

async function waitClosed() {
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
}

/** Runs the slash menu's Image item the way the suggestion plugin does. */
async function runSlashImage(editor: Editor) {
  const ext = editor.extensionManager.extensions.find((e) => e.name === "slashCommands")
  if (!ext) throw new Error("slash extension missing")
  const suggestion = ext.options.suggestion as {
    items: (o: { query: string }) => SlashCommandItem[]
    command: (o: { editor: Editor; range: Range; props: SlashCommandItem }) => void
  }
  const item = suggestion.items({ query: "image" }).find((i) => i.title === "Image")
  if (!item) throw new Error("Image slash item missing")
  // "/image" typed into an empty paragraph: positions 1..7.
  await act(async () => {
    suggestion.command({ editor, range: { from: 1, to: 7 }, props: item })
  })
}

describe("link dialog", () => {
  it("inserts a valid URL on the selected text and returns focus to the editor", async () => {
    const { editor, dom } = await renderEditor("hello world")
    act(() => {
      editor.commands.setTextSelection({ from: 1, to: 6 })
    })
    fireEvent.click(screen.getByTitle("Insert link"))

    expect(within(dialog()).getByText("Insert link")).toBeInTheDocument()
    typeUrl("https://example.com/docs")
    await pressEnter()
    await waitClosed()

    expect(editor.getHTML()).toContain('href="https://example.com/docs"')
    expect(editor.getHTML()).toMatch(/<a [^>]*>hello<\/a>/)
    expect(editor.state.selection.from).toBe(1)
    expect(editor.state.selection.to).toBe(6)
    await waitFor(() => expect(document.activeElement).toBe(dom))
    expect(promptSpy).not.toHaveBeenCalled()
  })

  it("shows an error for an invalid URL, keeps the dialog open and changes nothing", async () => {
    const { editor } = await renderEditor("hello world")
    act(() => {
      editor.commands.setTextSelection({ from: 1, to: 6 })
    })
    const before = editor.getHTML()
    fireEvent.click(screen.getByTitle("Insert link"))

    for (const bad of ["not a url", "javascript:alert(1)", "ftp://example.com"]) {
      typeUrl(bad)
      await pressEnter()
      expect(within(dialog()).getByRole("alert")).toHaveTextContent(/http/i)
      expect(urlInput()).toHaveAttribute("aria-invalid", "true")
    }
    expect(editor.getHTML()).toBe(before)

    // The error clears once the value is edited.
    typeUrl("https://example.com")
    expect(within(dialog()).queryByRole("alert")).toBeNull()
  })

  it("cancel and Esc leave the document and selection unchanged", async () => {
    const { editor, dom } = await renderEditor("hello world")
    act(() => {
      editor.commands.setTextSelection({ from: 1, to: 6 })
    })
    const before = editor.getHTML()

    fireEvent.click(screen.getByTitle("Insert link"))
    typeUrl("https://example.com")
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }))
    await waitClosed()
    expect(editor.getHTML()).toBe(before)

    fireEvent.click(screen.getByTitle("Insert link"))
    typeUrl("https://example.com")
    fireEvent.keyDown(urlInput(), { key: "Escape" })
    await waitClosed()
    expect(editor.getHTML()).toBe(before)
    expect(editor.state.selection.from).toBe(1)
    expect(editor.state.selection.to).toBe(6)
    await waitFor(() => expect(document.activeElement).toBe(dom))
  })

  it("pre-fills the current link when editing one", async () => {
    const { editor } = await renderEditor("[docs](https://old.example.com)")
    act(() => {
      editor.commands.setTextSelection(3)
    })
    fireEvent.click(screen.getByTitle("Insert link"))

    expect(within(dialog()).getByText("Edit link")).toBeInTheDocument()
    expect(urlInput().value).toBe("https://old.example.com")

    typeUrl("https://new.example.com")
    await pressEnter()
    await waitClosed()
    expect(editor.getHTML()).toContain('href="https://new.example.com"')
    expect(editor.getHTML()).not.toContain("old.example.com")
  })

  it("an empty value removes the link and keeps the text", async () => {
    const { editor } = await renderEditor("[docs](https://old.example.com)")
    act(() => {
      editor.commands.setTextSelection(3)
    })
    fireEvent.click(screen.getByTitle("Insert link"))
    typeUrl("   ")
    await pressEnter()
    await waitClosed()

    expect(editor.getHTML()).not.toContain("<a")
    expect(editor.getText()).toContain("docs")
  })
})

describe("toolbar image dialog", () => {
  it("inserts a valid image URL", async () => {
    const { editor } = await renderEditor("hello")
    fireEvent.click(screen.getByTitle("Insert image"))
    expect(within(dialog()).getByText("Insert image")).toBeInTheDocument()
    expect(urlInput().value).toBe("")

    typeUrl("https://example.com/a.png")
    fireEvent.click(within(dialog()).getByRole("button", { name: "Insert" }))
    await waitClosed()
    expect(editor.getHTML()).toContain('<img src="https://example.com/a.png"')
    expect(promptSpy).not.toHaveBeenCalled()
  })

  it("shows an error for an invalid or empty URL and inserts nothing", async () => {
    const { editor } = await renderEditor("hello")
    const before = editor.getHTML()
    fireEvent.click(screen.getByTitle("Insert image"))

    await pressEnter()
    expect(within(dialog()).getByRole("alert")).toBeInTheDocument()

    typeUrl("data:image/png;base64,AAAA")
    await pressEnter()
    expect(within(dialog()).getByRole("alert")).toHaveTextContent(/http/i)
    expect(editor.getHTML()).toBe(before)
  })

  it("cancel inserts nothing", async () => {
    const { editor } = await renderEditor("hello")
    const before = editor.getHTML()
    fireEvent.click(screen.getByTitle("Insert image"))
    typeUrl("https://example.com/a.png")
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }))
    await waitClosed()
    expect(editor.getHTML()).toBe(before)
  })
})

describe("slash command image dialog", () => {
  it("inserts a valid image URL in place of the slash command", async () => {
    const { editor } = await renderEditor("/image")
    await runSlashImage(editor)
    await waitFor(() => expect(dialog()).toBeInTheDocument())
    expect(editor.getText()).not.toContain("/image")

    typeUrl("https://example.com/b.png")
    await pressEnter()
    await waitClosed()
    expect(editor.getHTML()).toContain('<img src="https://example.com/b.png"')
    expect(promptSpy).not.toHaveBeenCalled()
  })

  it("shows an error for an invalid URL and inserts nothing", async () => {
    const { editor } = await renderEditor("/image")
    await runSlashImage(editor)
    await waitFor(() => expect(dialog()).toBeInTheDocument())

    typeUrl("example.com/b.png")
    await pressEnter()
    expect(within(dialog()).getByRole("alert")).toHaveTextContent(/http/i)
    expect(editor.getHTML()).not.toContain("<img")
  })

  it("cancel inserts nothing", async () => {
    const { editor } = await renderEditor("/image")
    await runSlashImage(editor)
    await waitFor(() => expect(dialog()).toBeInTheDocument())

    typeUrl("https://example.com/b.png")
    fireEvent.keyDown(urlInput(), { key: "Escape" })
    await waitClosed()
    expect(editor.getHTML()).not.toContain("<img")
  })
})
