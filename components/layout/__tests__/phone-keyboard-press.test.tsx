import { describe, it, expect, vi, afterEach } from "vitest"
import { render, cleanup, fireEvent, act } from "@testing-library/react"

import { PhoneKeyboard } from "../phone-keyboard"

// nightly on candidate16/17, 390 px: tapping Save in the save bar while a field
// had focus moved focus to the button, the tab bar came back at once and the
// bar jumped up before the click landed, so Save never fired. The layout must
// hold still until the press is over.
describe("PhoneKeyboard during a press", () => {
  afterEach(() => { cleanup(); vi.useRealTimers(); delete document.documentElement.dataset.typing })

  it("keeps data-typing until the press that took focus from the field ends", () => {
    vi.useFakeTimers()
    const { getByLabelText, getByRole } = render(
      <>
        <PhoneKeyboard />
        <label>Name<input /></label>
        <button type="button">Save</button>
      </>,
    )
    const input = getByLabelText("Name")
    const save = getByRole("button", { name: "Save" })
    act(() => { input.focus() })
    expect(document.documentElement.dataset.typing).toBe("")

    fireEvent.pointerDown(save)
    act(() => { save.focus() })
    act(() => { vi.runAllTimers() })
    // Still pressed: nothing may move under the finger.
    expect(document.documentElement.dataset.typing).toBe("")

    fireEvent.pointerUp(save)
    act(() => { vi.runAllTimers() })
    expect(document.documentElement.dataset.typing).toBeUndefined()
  })

  it("still drops data-typing at once when focus leaves without a press (Tab)", () => {
    vi.useFakeTimers()
    const { getByLabelText, getByRole } = render(
      <>
        <PhoneKeyboard />
        <label>Name<input /></label>
        <button type="button">Save</button>
      </>,
    )
    act(() => { getByLabelText("Name").focus() })
    act(() => { getByRole("button", { name: "Save" }).focus() })
    act(() => { vi.runAllTimers() })
    expect(document.documentElement.dataset.typing).toBeUndefined()
  })
})
