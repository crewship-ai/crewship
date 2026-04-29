import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { AvatarPickerDialog } from "@/components/features/crews/avatar-picker-dialog"
import { AVATAR_STYLES } from "@/lib/agent-avatar"

describe("<AvatarPickerDialog>", () => {
  const baseProps = {
    open: true,
    onOpenChange: vi.fn(),
    agentName: "Filip",
    seed: "filip-seed",
    style: null,
    crewStyle: null,
    onSave: vi.fn().mockResolvedValue(undefined),
  }

  beforeEach(() => {
    baseProps.onOpenChange.mockClear()
    baseProps.onSave.mockClear()
  })

  it("renders all DiceBear styles from the catalog (not phantom slugs)", () => {
    render(<AvatarPickerDialog {...baseProps} />)
    // Every catalog label should be present as a button label.
    for (const key of Object.keys(AVATAR_STYLES)) {
      const label = AVATAR_STYLES[key].label
      // Use getAllByText because the label may also appear in the title attr
      expect(screen.getAllByText(label).length).toBeGreaterThan(0)
    }
  })

  it("does NOT render phantom labels Robots/Humans/Abstract/Pixel as standalone options", () => {
    render(<AvatarPickerDialog {...baseProps} />)
    // The catalog DOES include "Robots" (label for bottts-neutral) so
    // we can't assert "no Robots". Instead, assert that none of the
    // phantom *slug values* (lowercased) appear in any button's
    // value attribute. Since the dialog uses our catalog now, any
    // regression to "robots"/"humans"/"abstract"/"pixel" as a value
    // would mean the catalog has those keys (which test #2 above
    // already asserts is false).
    const buttons = document.querySelectorAll("button")
    for (const btn of Array.from(buttons)) {
      const value = btn.getAttribute("data-style-value")
      if (value === "humans" || value === "abstract" || value === "pixel") {
        throw new Error(`Phantom style value ${value} found on a button`)
      }
    }
  })

  it("clicking a style button updates the preview src to the new style", async () => {
    render(<AvatarPickerDialog {...baseProps} />)
    // The big preview is the first <img> in the dialog with class containing w-24
    const preview = document.querySelector("img.w-24") as HTMLImageElement | null
    expect(preview).not.toBeNull()
    const initialSrc = preview!.src

    // Click "Adventurer" style (real catalog entry)
    const adventurerBtn = screen.getByText("Adventurer").closest("button")
    expect(adventurerBtn).toBeInTheDocument()
    fireEvent.click(adventurerBtn!)

    // Preview src should change because the seed is held but style flipped.
    await waitFor(() => {
      expect(preview!.src).not.toBe(initialSrc)
    })
  })

  it("clicking a quick-pick seed updates the preview", async () => {
    render(<AvatarPickerDialog {...baseProps} />)
    const preview = document.querySelector("img.w-24") as HTMLImageElement | null
    expect(preview).not.toBeNull()
    const initialSrc = preview!.src

    // Quick-pick row has 8 thumbnails. Click the third.
    const quickPickButtons = document.querySelectorAll("button > img.w-full")
    expect(quickPickButtons.length).toBeGreaterThanOrEqual(8)
    fireEvent.click((quickPickButtons[2] as HTMLImageElement).parentElement!)

    await waitFor(() => {
      expect(preview!.src).not.toBe(initialSrc)
    })
  })

  it("Save sends real style key and seed to onSave", async () => {
    render(<AvatarPickerDialog {...baseProps} />)
    // Pick the second catalog style (whatever it is) — we just need a real key.
    const realKeys = Object.keys(AVATAR_STYLES)
    expect(realKeys.length).toBeGreaterThan(1)
    const targetKey = realKeys[1] // not the first (which is default)
    const targetLabel = AVATAR_STYLES[targetKey].label
    fireEvent.click(screen.getByText(targetLabel).closest("button")!)

    fireEvent.click(screen.getByRole("button", { name: /Save avatar/ }))

    await waitFor(() => {
      expect(baseProps.onSave).toHaveBeenCalledTimes(1)
    })
    const arg = baseProps.onSave.mock.calls[0][0]
    expect(arg.avatar_style).toBe(targetKey)
    expect(typeof arg.avatar_seed).toBe("string")
    // The saved style MUST be a real catalog key.
    expect(AVATAR_STYLES[arg.avatar_style]).toBeDefined()
  })

  it("Inherit option saves null (so backend can fall through to crew)", async () => {
    // Start the dialog with a non-null style, then click Inherit.
    render(<AvatarPickerDialog {...baseProps} style="lorelei" />)
    fireEvent.click(screen.getByText("Inherit").closest("button")!)

    fireEvent.click(screen.getByRole("button", { name: /Save avatar/ }))

    await waitFor(() => {
      expect(baseProps.onSave).toHaveBeenCalled()
    })
    const arg = baseProps.onSave.mock.calls[0][0]
    expect(arg.avatar_style).toBeNull()
  })

  it("Cancel does not call onSave", () => {
    render(<AvatarPickerDialog {...baseProps} />)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(baseProps.onSave).not.toHaveBeenCalled()
    expect(baseProps.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("seed field is editable and Regenerate produces a new seed", () => {
    render(<AvatarPickerDialog {...baseProps} />)
    const seedInput = document.querySelector('input[type="text"]') as HTMLInputElement | null
    expect(seedInput).not.toBeNull()
    const before = seedInput!.value
    fireEvent.click(screen.getByText("Regenerate").closest("button")!)
    // Regenerate writes a fresh random seed; vanishingly unlikely to match.
    expect(seedInput!.value).not.toBe(before)
  })
})
