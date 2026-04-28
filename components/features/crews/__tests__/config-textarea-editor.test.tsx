import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { ConfigTextareaEditor } from "@/components/features/crews/config-textarea-editor"

describe("<ConfigTextareaEditor>", () => {
  it("renders empty state when value is null", () => {
    const onSave = vi.fn()
    render(
      <ConfigTextareaEditor
        format="json"
        filename="devcontainer.json"
        value={null}
        onSave={onSave}
      />,
    )
    expect(screen.getByText(/empty — click Add/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Add" })).toBeInTheDocument()
  })

  it("renders the value as preformatted text in read mode", () => {
    const onSave = vi.fn()
    render(
      <ConfigTextareaEditor
        format="json"
        filename="x.json"
        value={'{"a": 1}'}
        onSave={onSave}
      />,
    )
    expect(screen.getByText('{"a": 1}')).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument()
  })

  it("validates JSON live and disables Save on invalid input", async () => {
    const onSave = vi.fn()
    render(
      <ConfigTextareaEditor
        format="json"
        filename="x.json"
        value=""
        onSave={onSave}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Add" }))
    const ta = document.querySelector("textarea") as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: "{ broken" } })

    // Save button should be disabled (invalid JSON).
    const saveBtn = screen.getByRole("button", { name: "Save" }) as HTMLButtonElement
    expect(saveBtn.disabled).toBe(true)
  })

  it("dispatches Save with parsed value when JSON is valid", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <ConfigTextareaEditor
        format="json"
        filename="x.json"
        value=""
        onSave={onSave}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Add" }))
    const ta = document.querySelector("textarea") as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '{"image": "node:20"}' } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(onSave.mock.calls[0][0]).toBe('{"image": "node:20"}')
  })

  it("sends null to onSave when the textarea is cleared", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <ConfigTextareaEditor
        format="json"
        filename="x.json"
        value='{"a":1}'
        onSave={onSave}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    const ta = document.querySelector("textarea") as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onSave).toHaveBeenCalled())
    expect(onSave.mock.calls[0][0]).toBeNull()
  })

  it("Cancel reverts the draft and does not call onSave", () => {
    const onSave = vi.fn()
    render(
      <ConfigTextareaEditor
        format="toml"
        filename="mise.toml"
        value='[tools]\nnode = "20"'
        onSave={onSave}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    const ta = document.querySelector("textarea") as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: "GARBAGE" } })
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByText(/\[tools\]/)).toBeInTheDocument()
  })
})
