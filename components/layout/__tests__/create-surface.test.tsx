import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfacePill,
  CreateSurfaceRefusal,
  CreateSurfaceFooter,
  CreateSurfaceFrame,
  CreateSurfaceHeader,
  CreateSurfaceLoading,
  CreateSurfaceSteps,
} from "../create-surface"

// =============================================================================
// These assert the promises the shell makes on behalf of every surface that
// mounts it — the ones eleven hand-written dialogs each kept differently.
// =============================================================================

function Harness(props: {
  open?: boolean
  onOpenChange?: (o: boolean) => void
  onSubmit?: () => void
  onPrimary?: () => void
  onCancel?: () => void
  primaryDisabled?: boolean
  busy?: boolean
}) {
  return (
    <CreateSurface
      open={props.open ?? true}
      onOpenChange={props.onOpenChange ?? (() => {})}
      onSubmit={props.onSubmit}
      size="md"
    >
      <CreateSurfaceHeader
        context="Platform"
        title="New issue"
        onClose={() => props.onOpenChange?.(false)}
      />
      <CreateSurfaceBody>
        <input aria-label="Issue title" />
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        onCancel={props.onCancel ?? (() => props.onOpenChange?.(false))}
        primaryLabel="Create issue"
        onPrimary={props.onPrimary ?? (() => {})}
        primaryDisabled={props.primaryDisabled}
        busy={props.busy}
      />
    </CreateSurface>
  )
}

describe("CreateSurface", () => {
  beforeEach(() => cleanup())

  it("names itself from context and title, as one path", () => {
    render(<Harness />)
    // Not two headings side by side: the accessible name has to read
    // "Platform › New issue", not "PlatformNew issue" or just "New issue".
    //
    // The first version of this test asserted each word separately and passed
    // while the real computed name was "SMONew issue" — the chevron is an icon
    // and contributes nothing, so the two spans ran together. Assert the
    // SEPARATION, which is the part that was actually broken.
    const heading = screen.getByRole("heading", { level: 2 })
    expect(heading.textContent).not.toMatch(/PlatformNew/)
    expect(heading.textContent?.replace(/\s+/g, " ").trim()).toBe("Platform › New issue")
  })

  it("renders exactly one primary and always a Cancel", () => {
    render(<Harness />)
    // Cancel is not optional and not conditional — its absence on four of the
    // audited surfaces is why Esc was the only way out of them.
    expect(screen.getByRole("button", { name: /cancel/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /create issue/i })).toBeInTheDocument()
  })

  it("submits on ⌘↵ and on Ctrl+↵", () => {
    const onSubmit = vi.fn()
    render(<Harness onSubmit={onSubmit} />)
    const dialog = screen.getByRole("dialog")

    fireEvent.keyDown(dialog, { key: "Enter", metaKey: true })
    fireEvent.keyDown(dialog, { key: "Enter", ctrlKey: true })
    expect(onSubmit).toHaveBeenCalledTimes(2)
  })

  it("does not submit on a bare Enter", () => {
    // A bare Enter belongs to the focused field — a textarea needs it for a
    // newline, and a surface that submits on it eats half-typed descriptions.
    const onSubmit = vi.fn()
    render(<Harness onSubmit={onSubmit} />)
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Enter" })
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("fires the footer handlers", () => {
    const onPrimary = vi.fn()
    const onCancel = vi.fn()
    render(<Harness onPrimary={onPrimary} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole("button", { name: /create issue/i }))
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
    expect(onPrimary).toHaveBeenCalledTimes(1)
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it("locks Cancel as well as the primary while busy", () => {
    // Cancelling mid-write leaves the user unsure which side won.
    render(<Harness busy />)
    expect(screen.getByRole("button", { name: /create issue/i })).toBeDisabled()
    expect(screen.getByRole("button", { name: /cancel/i })).toBeDisabled()
  })

  it("closes from the header's close button", () => {
    const onOpenChange = vi.fn()
    render(<Harness onOpenChange={onOpenChange} />)
    fireEvent.click(screen.getByRole("button", { name: /close/i }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("renders nothing when closed", () => {
    render(<Harness open={false} />)
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })
})

describe("CreateSurfaceSteps", () => {
  beforeEach(() => cleanup())

  const STEPS = [
    { id: "identity", label: "Identity" },
    { id: "lineup", label: "Lineup" },
    { id: "review", label: "Review" },
  ]

  // Both layouts are in the DOM at once — CSS decides which one is visible, so
  // a bare getByText("Review") matches the chip AND the phone header. Query by
  // role: only the pointer-device layout renders its steps as buttons.
  const chip = (label: string) => screen.getByRole("button", { name: new RegExp(label) })

  it("marks the current step and lets you jump back to a finished one", () => {
    const onJump = vi.fn()
    render(<CreateSurfaceSteps steps={STEPS} current={2} onJump={onJump} />)

    expect(chip("Review")).toHaveAttribute("aria-current", "step")

    fireEvent.click(chip("Identity"))
    expect(onJump).toHaveBeenCalledWith(0)
  })

  it("refuses to skip forward", () => {
    // A step strip that lets you jump to step 4 from step 1 is a nav bar
    // pretending to be a wizard: the later steps depend on the earlier answers.
    const onJump = vi.fn()
    render(<CreateSurfaceSteps steps={STEPS} current={0} onJump={onJump} />)

    expect(chip("Review")).toBeDisabled()
    fireEvent.click(chip("Review"))
    expect(onJump).not.toHaveBeenCalled()
  })

  it("also renders the phone layout, as a labelled progress bar", () => {
    // Five chips do not fit at 390px, and a strip that scrolls sideways hides
    // exactly the information it exists to give.
    render(<CreateSurfaceSteps steps={STEPS} current={1} />)

    const bar = screen.getByRole("progressbar")
    expect(bar).toHaveAttribute("aria-valuenow", "2")
    expect(bar).toHaveAttribute("aria-valuemax", "3")
    expect(screen.getByText("2 / 3")).toBeInTheDocument()
  })
})

describe("CreateSurfaceFrame", () => {
  beforeEach(() => cleanup())

  it("renders the same header outside a Dialog", () => {
    // Regression: the header used Radix's DialogTitle unconditionally, and
    // those primitives THROW outside a Dialog root. The phone preview on
    // /design is exactly that — no Dialog — so every handset render crashed
    // into the route's error boundary.
    expect(() =>
      render(
        <CreateSurfaceFrame mobile>
          <CreateSurfaceHeader concept="crews" context="platform" title="New agent" onClose={vi.fn()} />
          <CreateSurfaceBody>
            <input aria-label="Agent name" />
          </CreateSurfaceBody>
          <CreateSurfaceFooter onCancel={vi.fn()} primaryLabel="Create agent" onPrimary={vi.fn()} />
        </CreateSurfaceFrame>,
      ),
    ).not.toThrow()

    expect(screen.getByRole("heading", { name: /New agent/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /create agent/i })).toBeInTheDocument()
  })

  it("marks itself so the parts can take the phone layout at any width", () => {
    // The preview has to force the sheet layout at desktop width; the parts
    // key off this attribute via group-data-[mobile=true]/surface.
    const { container } = render(
      <CreateSurfaceFrame mobile>
        <CreateSurfaceBody>body</CreateSurfaceBody>
      </CreateSurfaceFrame>,
    )
    expect(container.firstElementChild).toHaveAttribute("data-mobile", "true")
  })

  it("does not claim to be mobile when it is not", () => {
    const { container } = render(
      <CreateSurfaceFrame>
        <CreateSurfaceBody>body</CreateSurfaceBody>
      </CreateSurfaceFrame>,
    )
    expect(container.firstElementChild).not.toHaveAttribute("data-mobile")
  })
})

describe("the discard guard", () => {
  beforeEach(() => cleanup())

  function Dirty({ dirty, onOpenChange }: { dirty: boolean; onOpenChange: (o: boolean) => void }) {
    return (
      <CreateSurface open onOpenChange={onOpenChange} dirty={dirty} discardLabel="this issue">
        <CreateSurfaceHeader title="New issue" onClose={() => onOpenChange(false)} />
        <CreateSurfaceBody>
          <input aria-label="Issue title" />
        </CreateSurfaceBody>
        <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel="Create" onPrimary={vi.fn()} />
      </CreateSurface>
    )
  }

  it("closes straight away when there is nothing to lose", () => {
    const onOpenChange = vi.fn()
    render(<Dirty dirty={false} onOpenChange={onOpenChange} />)
    fireEvent.click(screen.getByRole("button", { name: /close/i }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
  })

  it("asks before throwing away unsaved input", () => {
    const onOpenChange = vi.fn()
    render(<Dirty dirty onOpenChange={onOpenChange} />)

    fireEvent.click(screen.getByRole("button", { name: /close/i }))
    // Not closed yet — the guard intercepted.
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(screen.getByRole("alertdialog")).toHaveTextContent(/discard this issue/i)
  })

  it("keeps editing when you say so", () => {
    const onOpenChange = vi.fn()
    render(<Dirty dirty onOpenChange={onOpenChange} />)
    fireEvent.click(screen.getByRole("button", { name: /close/i }))
    fireEvent.click(screen.getByRole("button", { name: /keep editing/i }))
    expect(onOpenChange).not.toHaveBeenCalled()
  })

  it("closes once you confirm", () => {
    const onOpenChange = vi.fn()
    render(<Dirty dirty onOpenChange={onOpenChange} />)
    fireEvent.click(screen.getByRole("button", { name: /close/i }))
    fireEvent.click(screen.getByRole("button", { name: /^discard$/i }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  // Reported from a screenshot: the guard arrived in the primitive's own
  // clothes — bg-background, p-6, a 512px card and a blue confirm — on top of
  // the surface it belongs to, at the moment the user is deciding whether to
  // lose work. It is the same card as the surface now.
  it("wears the surface's chrome, not the primitive's defaults", () => {
    render(<Dirty dirty onOpenChange={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /close/i }))

    const alert = screen.getByRole("alertdialog")
    expect(alert.className).toContain("bg-card")
    expect(alert.className).toContain("border-hairline")
    // The width override has to carry the primitive's own data-size variant,
    // or the two rules tie and source order leaves the card at max-w-lg.
    expect(alert.className).toContain("data-[size=default]:sm:max-w-[420px]")
    // Its own padding is zero: the header and footer bring the surface's.
    expect(alert.className).toContain("p-0")
  })

  it("makes the destructive answer look destructive", () => {
    render(<Dirty dirty onOpenChange={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /close/i }))

    const discard = screen.getByRole("button", { name: /^discard$/i })
    expect(discard.className).toMatch(/destructive/)
    // And the way back out is not competing with it for attention.
    const keep = screen.getByRole("button", { name: /keep editing/i })
    expect(keep.className).not.toMatch(/bg-primary/)
  })
})

describe("CreateSurfaceRefusal", () => {
  beforeEach(() => cleanup())

  it("renders nothing until the server has said no", () => {
    const { container } = render(<CreateSurfaceRefusal message={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("announces itself, because it appears while you are looking elsewhere", () => {
    render(<CreateSurfaceRefusal message="That slug is taken." />)
    const alert = screen.getByRole("alert")
    expect(alert).toHaveAttribute("aria-live", "assertive")
    expect(alert).toHaveTextContent("That slug is taken.")
  })

  it("renders a field list as rows, not as a sentence", () => {
    // A 400 naming three fields is a worklist. Prose makes you decode it.
    render(
      <CreateSurfaceRefusal
        message="Three fields were refused."
        fields={[
          { field: "slug", reason: "already in use" },
          { field: "container_cpus", reason: "must be between 0.01 and 512" },
        ]}
      />,
    )
    expect(screen.getByText("slug")).toBeInTheDocument()
    expect(screen.getByText(/already in use/)).toBeInTheDocument()
    expect(screen.getByText("container_cpus")).toBeInTheDocument()
  })

  it("offers retry only when it is given one", () => {
    const onRetry = vi.fn()
    const { rerender } = render(<CreateSurfaceRefusal message="Server error." onRetry={onRetry} />)
    fireEvent.click(screen.getByRole("button", { name: /try again/i }))
    expect(onRetry).toHaveBeenCalledTimes(1)

    // A 400 is not retryable — the caller omits onRetry and no button appears.
    rerender(<CreateSurfaceRefusal message="That slug is taken." />)
    expect(screen.queryByRole("button", { name: /try again/i })).not.toBeInTheDocument()
  })
})

describe("CreateSurfaceLoading", () => {
  beforeEach(() => cleanup())

  it("says it is busy to a screen reader, not just to the eye", () => {
    render(<CreateSurfaceLoading />)
    expect(screen.getByText("Loading")).toBeInTheDocument()
  })
})

// =============================================================================
// The four defects eleven parallel migrations found in this shell. Each of
// these fails on the version that shipped in the first commit.
// =============================================================================

describe("what the migrations caught", () => {
  beforeEach(() => cleanup())

  it("does not echo the title into the accessible tree", () => {
    // It used to render <DialogDescription className="sr-only">{title}</> to
    // silence Radix's warning. Screen readers said "New issue. New issue.",
    // and getByText(title) matched twice — two agents had to rewrite existing
    // assertions that were not wrong.
    render(
      <CreateSurface open onOpenChange={vi.fn()}>
        <CreateSurfaceHeader title="New issue" onClose={vi.fn()} />
        <CreateSurfaceBody>body</CreateSurfaceBody>
      </CreateSurface>,
    )
    expect(screen.getAllByText("New issue")).toHaveLength(1)
    expect(screen.getByRole("dialog")).not.toHaveAttribute("aria-describedby")
  })

  it("keeps an interactive context visible, because a phone still has to set it", () => {
    // New issue's `context` IS the crew selector and crew is required, so
    // hiding it below `sm` shipped a surface whose landing place could not be
    // chosen on a phone — and a child cannot un-hide itself under display:none.
    render(
      <CreateSurface open onOpenChange={vi.fn()}>
        <CreateSurfaceHeader
          keepContext
          context={<button type="button">Pick crew</button>}
          title="New issue"
          onClose={vi.fn()}
        />
        <CreateSurfaceBody>body</CreateSurfaceBody>
      </CreateSurface>,
    )
    const wrapper = screen.getByRole("button", { name: "Pick crew" }).parentElement
    expect(wrapper?.className ?? "").not.toContain("max-sm:hidden")
  })

  it("focuses itself on open so ⌘↵ is not dead before the first click", () => {
    // The shortcut moved from a window listener onto the content's onKeyDown,
    // and the shell suppresses Radix's auto-focus — so a surface with no
    // autofocused field left focus on the opener and the shortcut did nothing.
    const onSubmit = vi.fn()
    render(
      <CreateSurface open onOpenChange={vi.fn()} onSubmit={onSubmit}>
        <CreateSurfaceHeader title="Add integration" onClose={vi.fn()} />
        <CreateSurfaceBody>no fields here</CreateSurfaceBody>
      </CreateSurface>,
    )
    const dialog = screen.getByRole("dialog")
    expect(document.activeElement).toBe(dialog)

    fireEvent.keyDown(document.activeElement!, { key: "Enter", metaKey: true })
    expect(onSubmit).toHaveBeenCalledTimes(1)
  })

  it("names every step, which is how an e2e selector finds one", () => {
    // Naming chips by visible text alone broke e2e/create-crew-wizard.spec.ts
    // (getByLabel("Step 1: Identity")) and forced six unit edits.
    render(
      <CreateSurfaceSteps
        ariaLabel="Wizard progress"
        current={1}
        steps={[
          { id: "identity", label: "Identity" },
          { id: "lineup", label: "Lineup" },
        ]}
      />,
    )
    expect(screen.getByRole("navigation", { name: "Wizard progress" })).toBeInTheDocument()
    expect(screen.getByLabelText("Step 1: Identity")).toBeInTheDocument()
    expect(screen.getByLabelText("Step 2: Lineup")).toBeInTheDocument()
  })
})

describe("what the migrations asked for", () => {
  beforeEach(() => cleanup())

  it("renders a footer with no primary — and still a Cancel", () => {
    // A Pick surface whose action is the tile used to get no footer at all,
    // and therefore no Cancel, contradicting this shell's own rule.
    const onCancel = vi.fn()
    render(<CreateSurfaceFooter onCancel={onCancel} />)
    expect(screen.getByRole("button", { name: /cancel/i })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /create/i })).not.toBeInTheDocument()
  })

  it("routes Cancel through the guard only when asked", () => {
    const onOpenChange = vi.fn()
    const onCancel = vi.fn()
    render(
      <CreateSurface open onOpenChange={onOpenChange} dirty>
        <CreateSurfaceHeader title="New crew" onClose={vi.fn()} />
        <CreateSurfaceBody>body</CreateSurfaceBody>
        <CreateSurfaceFooter guardCancel onCancel={onCancel} primaryLabel="Create" onPrimary={vi.fn()} />
      </CreateSurface>,
    )
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
    expect(onCancel).not.toHaveBeenCalled()
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
  })

  it("carries a non-error band, because a dry run also passes", () => {
    render(<CreateSurfaceRefusal tone="ok" message="Dry run passed." />)
    const band = screen.getByRole("status")
    expect(band).toHaveTextContent("Dry run passed.")
    expect(band).toHaveAttribute("aria-live", "polite")
  })

  it("gives a field refusal a third rank instead of flattening it", () => {
    render(
      <CreateSurfaceRefusal
        message="One reference could not be bound."
        fields={[{ field: "routine:nightly-sweep", reason: "no routine of that slug", detail: "(used by 2 panels)" }]}
      />,
    )
    expect(screen.getByText("(used by 2 panels)")).toBeInTheDocument()
  })

  it("lets a pill carry a prop-driven glyph the icon slot cannot type", () => {
    render(
      <CreateSurfacePill leading={<span data-testid="swatch" />}>Urgent</CreateSurfacePill>,
    )
    expect(screen.getByTestId("swatch")).toBeInTheDocument()
  })

  it("lets a body opt out of being the scrollport", () => {
    const { container } = render(
      <CreateSurfaceBody padded={false} scroll={false}>
        editor
      </CreateSurfaceBody>,
    )
    const cls = container.firstElementChild?.className ?? ""
    expect(cls).not.toContain("overflow-y-auto")
    expect(cls).not.toContain("sm:px-5")
  })
})
