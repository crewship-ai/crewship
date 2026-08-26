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
    // Keyed on a testid, not a Tailwind size class: the picker has been
    // resized once already (it ran past the surface's height and cut the
    // quick picks off the bottom) and a size class is not what this test is
    // about.
    const preview = screen.getByTestId("avatar-preview") as HTMLImageElement
    expect(preview).not.toBeNull()
    const initialSrc = preview.src

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
    const preview = screen.getByTestId("avatar-preview") as HTMLImageElement
    expect(preview).not.toBeNull()
    const initialSrc = preview!.src

    // Quick-pick row has 8 thumbnails. Click the third.
    const quickPickButtons = screen.getByTestId("avatar-quick-pick").querySelectorAll("button")
    expect(quickPickButtons.length).toBe(8)
    fireEvent.click(quickPickButtons[2])

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

  it("following the crew saves null (so backend can fall through to crew)", async () => {
    // Start the dialog with a non-null style, then choose to follow the crew.
    // The control is labelled "Follow the crew" rather than "Inherit": it is a
    // reference, not a twelfth style, and its old tile drew the identical face
    // as the Robots tile whenever the crew had no style of its own.
    render(<AvatarPickerDialog {...baseProps} style="lorelei" />)
    fireEvent.click(screen.getByRole("button", { name: /Follow the crew/i }))

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

  it("keeps every control reachable without the panel outgrowing the surface", () => {
    // The four blocks used to stack full-width — a 96px preview, 25 tiles of
    // 40px faces with labels, eight quick picks each a full grid column wide,
    // and a seed field under its own heading. Past 650px the quick picks fell
    // off the bottom of the create surface and could not be scrolled to.
    //
    // jsdom computes no layout, so this cannot assert pixels. What it can
    // assert is the structural decisions that produced the height: the style
    // grid is the only scrollport, and the seed controls share a row with the
    // preview instead of being a fifth block below the grid.
    render(<AvatarPickerDialog {...baseProps} />)

    const grid = screen.getByTestId("avatar-style-grid")
    expect(grid.className).toContain("overflow-y-auto")
    expect(grid.className).toMatch(/max-h-\[\d+px\]/)

    // Preview, seed input and quick picks in one subtree — the grid is not
    // between them.
    const row = screen.getByTestId("avatar-preview").parentElement!
    expect(row).toContainElement(screen.getByLabelText("Avatar seed"))
    expect(row).toContainElement(screen.getByTestId("avatar-quick-pick"))
    expect(row).not.toContainElement(grid)
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
