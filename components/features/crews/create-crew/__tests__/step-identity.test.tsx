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

  it("clicking the icon tile opens the picker dialog", () => {
    harness()
    // Picker dialog isn't mounted until opened. Click triggers open via portal.
    const tile = screen.getByLabelText("Pick icon and color")
    fireEvent.click(tile)
    // Dialog title is "Icon — <crewName>" — find it.
    expect(screen.getByText(/^Icon —/)).toBeInTheDocument()
  })

  it("Slug field shows current slug in the TIP example", () => {
    harness({ slug: "research-team" })
    expect(screen.getByText(/--crew research-team/)).toBeInTheDocument()
  })
})
