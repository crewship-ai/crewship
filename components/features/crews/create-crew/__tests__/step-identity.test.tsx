import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { StepIdentity } from "../step-identity"
import { INITIAL_STATE, type WizardState } from "../types"

function harness(initial: Partial<WizardState> = {}) {
  let state: WizardState = { ...INITIAL_STATE, ...initial }
  const setState = vi.fn((patch: Partial<WizardState>) => {
    state = { ...state, ...patch }
  })
  const renderResult = render(<StepIdentity state={state} setState={setState} />)
  return {
    ...renderResult,
    setState,
    rerenderWith: (patch: Partial<WizardState>) => {
      state = { ...state, ...patch }
      renderResult.rerender(<StepIdentity state={state} setState={setState} />)
    },
    getState: () => state,
  }
}

describe("<StepIdentity>", () => {
  it("renders Name, Slug, and Description inputs", () => {
    harness()
    expect(screen.getByPlaceholderText("Engineering")).toBeInTheDocument()
    expect(screen.getByPlaceholderText("engineering")).toBeInTheDocument()
    expect(screen.getByPlaceholderText(/What does this crew do/)).toBeInTheDocument()
  })

  it("auto-derives slug from name on first edit", () => {
    const { setState } = harness()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), {
      target: { value: "Customer Support" },
    })

    expect(setState).toHaveBeenCalledWith({
      name: "Customer Support",
      slug: "customer-support",
    })
  })

  it("strips non-alphanumeric chars from auto-slug (spaces, &, /, accents)", () => {
    const { setState } = harness()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), {
      target: { value: "Sales & Ops / Q1!!" },
    })

    const lastCall = setState.mock.calls[setState.mock.calls.length - 1]?.[0]
    expect(lastCall?.slug).toMatch(/^[a-z0-9-]+$/)
    expect(lastCall?.slug).not.toMatch(/^-|-$/) // no leading/trailing hyphen
  })

  it("collapses runs of separators to single hyphen", () => {
    const { setState } = harness()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), {
      target: { value: "Foo   ---   Bar" },
    })

    const lastCall = setState.mock.calls[setState.mock.calls.length - 1]?.[0]
    expect(lastCall?.slug).not.toContain("--")
  })

  it("stops auto-deriving slug once user manually edits the slug", () => {
    const { setState, rerenderWith } = harness()

    // User types a name → slug auto-derives
    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Foo" } })
    rerenderWith({ name: "Foo", slug: "foo" })

    // User manually edits slug
    fireEvent.change(screen.getByPlaceholderText("engineering"), { target: { value: "custom-slug" } })
    rerenderWith({ slug: "custom-slug", slugTouched: true })

    // Now further name changes should NOT touch slug
    setState.mockClear()
    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Foo Bar" } })

    const namePatch = setState.mock.calls[0][0]
    expect(namePatch).toEqual({ name: "Foo Bar" }) // slug NOT included
  })

  it("description input writes through to setState", () => {
    const { setState } = harness()

    fireEvent.change(screen.getByPlaceholderText(/What does this crew do/), {
      target: { value: "Backend services" },
    })

    expect(setState).toHaveBeenCalledWith({ description: "Backend services" })
  })

  it("renders the icon-tile button using current state", () => {
    harness({ icon: "rocket", color: "violet" })
    // Caption beneath the tile shows "icon · color"
    expect(screen.getByText(/rocket · violet/)).toBeInTheDocument()
  })

  it("clicking the icon tile opens the picker", () => {
    harness()
    const tile = screen.getByLabelText("Pick icon and color")
    fireEvent.click(tile)
    // In the body now, not a portalled dialog with its own "Icon — <crew>"
    // title: the picker's search box is the thing that proves it is open.
    expect(screen.getByPlaceholderText(/search icons/i)).toBeInTheDocument()
  })

  it("Slug field shows current slug in the TIP example", () => {
    harness({ slug: "research-team" })
    expect(screen.getByText(/--crew research-team/)).toBeInTheDocument()
  })
})

// ── The icon picker is not a second dialog ────────────────────────────────
//
// It used to mount CrewIconPickerDialog — a full Radix <Dialog> — from inside
// the wizard's own Dialog, so opening it put two overlays, two headers and two
// Cancel buttons on screen. That is the shape the whole create-surface
// migration exists to remove, and it survived it.
describe("<StepIdentity> icon picker", () => {
  it("opens in the body rather than stacking another dialog", () => {
    const setState = vi.fn()
    render(<StepIdentity state={{ ...INITIAL_STATE }} setState={setState} />)

    fireEvent.click(screen.getByRole("button", { name: /pick icon and color/i }))

    // The kit's picker, in this surface's own body.
    expect(screen.getByPlaceholderText(/search icons/i)).toBeInTheDocument()
    // And no second dialog anywhere.
    expect(screen.queryAllByRole("dialog")).toHaveLength(0)
  })

  it("closes again from the same control it opened from", () => {
    render(<StepIdentity state={{ ...INITIAL_STATE }} setState={vi.fn()} />)
    const tile = screen.getByRole("button", { name: /pick icon and color/i })

    fireEvent.click(tile)
    expect(tile).toHaveAttribute("aria-expanded", "true")
    fireEvent.click(tile)
    expect(tile).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByPlaceholderText(/search icons/i)).toBeNull()
  })

  it("patches the icon and the colour separately, as the wizard state expects", () => {
    const setState = vi.fn()
    render(<StepIdentity state={{ ...INITIAL_STATE }} setState={setState} />)
    fireEvent.click(screen.getByRole("button", { name: /pick icon and color/i }))

    fireEvent.click(screen.getByRole("radio", { name: "Rocket" }))
    expect(setState).toHaveBeenCalledWith({ icon: "rocket" })
  })

  it("searches the icon set instead of making you scroll it", () => {
    render(<StepIdentity state={{ ...INITIAL_STATE }} setState={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /pick icon and color/i }))

    const before = screen.getAllByRole("radio").length
    fireEvent.change(screen.getByPlaceholderText(/search icons/i), { target: { value: "rocket" } })
    const after = screen.getAllByRole("radio").length
    expect(after).toBeLessThan(before)
    expect(screen.getByRole("radio", { name: "Rocket" })).toBeInTheDocument()
  })
})
