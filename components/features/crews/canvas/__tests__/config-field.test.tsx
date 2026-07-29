import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

import {
  ConfigText,
  ConfigSelect,
  ConfigSwitch,
  ConfigPresets,
} from "@/components/features/crews/canvas/config-field"

const toastError = vi.fn()
vi.mock("sonner", () => ({ toast: { error: (...a: unknown[]) => toastError(...a) } }))

beforeEach(() => {
  toastError.mockClear()
})

describe("<ConfigText>", () => {
  it("saves on blur when the value changed", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(<ConfigText label="Jméno" value="Morgan" onSave={onSave} />)

    const input = screen.getByLabelText("Jméno")
    fireEvent.change(input, { target: { value: "Morgan Q" } })
    fireEvent.blur(input)

    await waitFor(() => expect(onSave).toHaveBeenCalledWith("Morgan Q"))
    expect(onSave).toHaveBeenCalledTimes(1)
  })

  it("does not save when the value is unchanged", () => {
    const onSave = vi.fn()
    render(<ConfigText label="Jméno" value="Morgan" onSave={onSave} />)

    const input = screen.getByLabelText("Jméno")
    fireEvent.focus(input)
    fireEvent.blur(input)

    expect(onSave).not.toHaveBeenCalled()
  })

  it("reverts on Escape without saving", () => {
    const onSave = vi.fn()
    render(<ConfigText label="Jméno" value="Morgan" onSave={onSave} />)

    const input = screen.getByLabelText("Jméno") as HTMLInputElement
    fireEvent.change(input, { target: { value: "smazáno" } })
    fireEvent.keyDown(input, { key: "Escape" })

    expect(input.value).toBe("Morgan")
    expect(onSave).not.toHaveBeenCalled()
  })

  it("restores the previous value and reports when the save fails", async () => {
    const onSave = vi.fn().mockRejectedValue(new Error("slug already taken"))
    render(<ConfigText label="Slug" value="morgan" onSave={onSave} />)

    const input = screen.getByLabelText("Slug") as HTMLInputElement
    fireEvent.change(input, { target: { value: "riley" } })
    fireEvent.blur(input)

    await waitFor(() => expect(input.value).toBe("morgan"))
    expect(toastError).toHaveBeenCalled()
  })

  it("follows the record when the value prop changes from outside", () => {
    const { rerender } = render(<ConfigText label="Jméno" value="Morgan" onSave={vi.fn()} />)
    rerender(<ConfigText label="Jméno" value="Quinn" onSave={vi.fn()} />)
    expect((screen.getByLabelText("Jméno") as HTMLInputElement).value).toBe("Quinn")
  })

  it("renders the hint next to the label", () => {
    render(<ConfigText label="Slug" hint="Používá se v CLI." value="morgan" onSave={vi.fn()} />)
    expect(screen.getByText("Používá se v CLI.")).toBeInTheDocument()
  })
})

describe("<ConfigSelect>", () => {
  const options = [
    { value: "CODING", label: "CODING" },
    { value: "MINIMAL", label: "MINIMAL" },
  ]

  it("saves immediately on change", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(<ConfigSelect label="Tool profile" value="CODING" options={options} onSave={onSave} />)

    fireEvent.change(screen.getByLabelText("Tool profile"), { target: { value: "MINIMAL" } })
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("MINIMAL"))
  })

  it("reverts the selection when the save fails", async () => {
    const onSave = vi.fn().mockRejectedValue(new Error("nope"))
    render(<ConfigSelect label="Tool profile" value="CODING" options={options} onSave={onSave} />)

    const select = screen.getByLabelText("Tool profile") as HTMLSelectElement
    fireEvent.change(select, { target: { value: "MINIMAL" } })

    await waitFor(() => expect(select.value).toBe("CODING"))
    expect(toastError).toHaveBeenCalled()
  })
})

describe("<ConfigSwitch>", () => {
  it("saves the flipped value", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(<ConfigSwitch label="Paměť" checked={false} onSave={onSave} />)

    fireEvent.click(screen.getByRole("switch", { name: "Paměť" }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(true))
  })

  it("flips back when the save fails", async () => {
    const onSave = vi.fn().mockRejectedValue(new Error("nope"))
    render(<ConfigSwitch label="Paměť" checked={false} onSave={onSave} />)

    const sw = screen.getByRole("switch", { name: "Paměť" })
    fireEvent.click(sw)
    await waitFor(() => expect(sw).toHaveAttribute("aria-checked", "false"))
  })

  it("does not save while locked and marks itself disabled", () => {
    const onSave = vi.fn()
    render(<ConfigSwitch label="Privátní endpointy" checked={false} locked onSave={onSave} />)

    const sw = screen.getByRole("switch", { name: "Privátní endpointy" })
    fireEvent.click(sw)
    expect(onSave).not.toHaveBeenCalled()
    expect(sw).toBeDisabled()
  })
})

describe("<ConfigPresets>", () => {
  const presets = [
    { value: 300, label: "5 m" },
    { value: 1800, label: "30 m" },
    { value: 3600, label: "1 h" },
  ]

  it("marks the option matching the current value", () => {
    render(<ConfigPresets label="Nejdelší běh" value={1800} presets={presets} onSave={vi.fn()} />)
    expect(screen.getByRole("button", { name: "30 m" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: "5 m" })).toHaveAttribute("aria-pressed", "false")
  })

  it("saves the picked preset value, not its label", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(<ConfigPresets label="Nejdelší běh" value={1800} presets={presets} onSave={onSave} />)

    fireEvent.click(screen.getByRole("button", { name: "1 h" }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(3600))
  })

  it("keeps a value that matches no preset visible as a custom chip", () => {
    render(<ConfigPresets label="Nejdelší běh" value={900} presets={presets} onSave={vi.fn()} />)
    expect(screen.getByRole("button", { name: /vlastní/i })).toHaveAttribute("aria-pressed", "true")
  })
})
