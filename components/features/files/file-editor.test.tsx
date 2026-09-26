import { act, render } from "@testing-library/react"
import { EditorView } from "@codemirror/view"
import { EditorState } from "@codemirror/state"
import { expect, it, vi } from "vitest"
import { FileEditor } from "./file-editor"

it("locks and unlocks the existing CodeMirror document without resetting edits", () => {
  const onSave = vi.fn()
  const { container, rerender } = render(<FileEditor code="original" language="text" onSave={onSave} />)
  const view = EditorView.findFromDOM(container.querySelector(".cm-editor")!)!
  act(() => view.dispatch({ changes: { from: 0, to: 8, insert: "unsaved edits" } }))
  rerender(<FileEditor code="original" language="text" onSave={onSave} readOnly />)
  expect(EditorView.findFromDOM(container.querySelector(".cm-editor")!)).toBe(view)
  expect(view.state.facet(EditorState.readOnly)).toBe(true)
  expect(view.state.doc.toString()).toBe("unsaved edits")
  rerender(<FileEditor code="original" language="text" onSave={onSave} readOnly={false} />)
  expect(view.state.facet(EditorState.readOnly)).toBe(false)
  expect(view.state.doc.toString()).toBe("unsaved edits")
})
