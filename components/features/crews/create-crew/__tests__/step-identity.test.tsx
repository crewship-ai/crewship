import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { StepIdentity } from "../step-identity"
import { INITIAL_STATE, type WizardState } from "../types"

function harness(initial: Partial<WizardState> = {}) {
  let state: WizardState = { ...INITIAL_STATE, ...initial }
  const setState = vi.fn((patch: Partial<WizardState>) => {
    state = { ...state, ...patch }
  })
  const onPickIcon = vi.fn()
  const renderResult = render(
    <StepIdentity state={state} setState={setState} onPickIcon={onPickIcon} />,
  )
  return {
    ...renderResult,
    setState,
    onPickIcon,
    rerenderWith: (patch: Partial<WizardState>) => {
      state = { ...state, ...patch }
      renderResult.rerender(
        <StepIdentity state={state} setState={setState} onPickIcon={onPickIcon} />,
      )
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
    // The caption names the icon the way the catalogue does — "rocket" is the
    // key, "Rocket" is what a person reads.
    expect(screen.getByText(/Rocket · violet/)).toBeInTheDocument()
  })

  it("Slug field shows current slug in the TIP example", () => {
    harness({ slug: "research-team" })
    expect(screen.getByText(/--crew research-team/)).toBeInTheDocument()
  })
})

// ── The icon picker is a panel, not part of this step ───────────────────
//
// It started as CrewIconPickerDialog — a full Radix <Dialog> opened from
// inside the wizard's own — then became an inline block on this step, which
// put the form, a notice, a preview, a colour row, a search box and a grid of
// 345 icons on one screen. New project had already solved this: the surface
// SWAPS to the picker, with its own header, back arrow and "Use this icon".
// This step asks for the panel and draws none of it.
describe("<StepIdentity> — the icon control", () => {
  it("asks the wizard for the panel rather than opening one here", () => {
    const { onPickIcon } = harness()
    fireEvent.click(screen.getByRole("button", { name: /pick icon and color/i }))
    expect(onPickIcon).toHaveBeenCalledTimes(1)
  })

  it("draws no picker of its own", () => {
    harness()
    fireEvent.click(screen.getByRole("button", { name: /pick icon and color/i }))
    expect(screen.queryByPlaceholderText(/search icons/i)).toBeNull()
    expect(screen.queryAllByRole("dialog")).toHaveLength(0)
  })

  it("offers the caption as a second way in, for the same panel", () => {
    const { onPickIcon } = harness({ icon: "rocket", color: "violet" })
    fireEvent.click(screen.getByText(/Rocket · violet/))
    expect(onPickIcon).toHaveBeenCalled()
  })
})
