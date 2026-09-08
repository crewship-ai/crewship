import { createRef, useRef, useState } from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { MentionAutocomplete } from "../mention-autocomplete"

afterEach(cleanup)

const members = [
  { id: "agent-1", slug: "marena", name: "Mařena" },
  { id: "agent-2", slug: "mia", name: "Mia" },
]

function setup() {
  const ref = createRef<HTMLTextAreaElement>()
  const onPick = vi.fn()
  const onSubmit = vi.fn()
  render(
    <div onKeyDown={(event) => { if (event.key === "Enter") onSubmit() }}>
      <textarea ref={ref} aria-label="Message" />
      <MentionAutocomplete text="" textareaRef={ref} members={members} onPick={onPick} />
    </div>,
  )
  const textarea = screen.getByRole("textbox")
  fireEvent.input(textarea, { target: { value: "@", selectionStart: 1 } })
  return { textarea, onPick, onSubmit }
}

function ControlledPicker() {
  const ref = useRef<HTMLTextAreaElement>(null)
  const [text, setText] = useState("")
  return <>
    <textarea aria-label="Message" ref={ref} value={text} onChange={(event) => setText(event.target.value)} />
    <button onClick={() => setText("")}>Clear after send</button>
    <MentionAutocomplete text={text} textareaRef={ref} members={members} onPick={(member) => setText(`@${member.slug} `)} />
  </>
}

describe("mention keyboard interaction", () => {
  it("retains ArrowDown selection through keyup and picks without sending the message", () => {
    const { textarea, onPick, onSubmit } = setup()
    fireEvent.keyDown(textarea, { key: "ArrowDown" })
    fireEvent.keyUp(textarea, { key: "ArrowDown" })
    fireEvent.keyDown(textarea, { key: "Enter" })
    expect(onPick).toHaveBeenCalledWith(members[1], 0)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("stays dismissed after Escape keyup", () => {
    const { textarea } = setup()
    expect(screen.getByText(/Mention an agent/)).toBeInTheDocument()
    fireEvent.keyDown(textarea, { key: "Escape" })
    fireEvent.keyUp(textarea, { key: "Escape" })
    expect(screen.queryByText(/Mention an agent/)).not.toBeInTheDocument()
  })
  it("dismisses a stale picker when the controlled text is cleared", () => {
    render(<ControlledPicker />)
    fireEvent.input(screen.getByRole("textbox"), { target: { value: "@", selectionStart: 1 } })
    expect(screen.getByRole("listbox")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear after send" }))
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" })
    expect(screen.getByRole("textbox")).toHaveValue("")
  })

  it("does not reopen after selecting a controlled mention", () => {
    render(<ControlledPicker />)
    fireEvent.input(screen.getByRole("textbox"), { target: { value: "@", selectionStart: 1 } })
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" })
    expect(screen.getByRole("textbox")).toHaveValue("@marena ")
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument()
  })

})
