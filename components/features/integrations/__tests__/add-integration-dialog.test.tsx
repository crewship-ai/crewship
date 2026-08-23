import { describe, it, expect, vi, beforeEach } from "vitest"
import type { ComponentProps } from "react"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import { AddIntegrationDialog, type ServiceOption } from "../add-integration-dialog"
import type { NotificationProviderCategory } from "@/hooks/use-notification-channels"

// =============================================================================
// "Add integration" on the shared create shell.
//
// The surface never talks to the server itself: its primary action is handing
// the host a chosen service (which then opens that service's form) or handing
// it the tools tab. So "issues the same request" here means "calls the same
// callback, with the same argument, after closing itself" — that hand-off is
// the contract the migration may not move.
// =============================================================================

const SECTIONS: NotificationProviderCategory[] = [
  { key: "chat", label: "Chat", hint: "where a team already talks" },
  { key: "push", label: "Push" },
]

const SERVICES: ServiceOption[] = [
  {
    key: "slack",
    label: "Slack",
    blurb: "Channels, threads and approvals",
    section: "chat",
    available: true,
    used: 2,
  },
  {
    key: "discord",
    label: "Discord",
    blurb: "Servers and channels",
    section: "chat",
    available: false,
    used: 0,
  },
  {
    key: "ntfy",
    label: "ntfy",
    blurb: "Push to a phone without an app account",
    section: "push",
    available: true,
    used: 0,
  },
]

function renderDialog(over: Partial<ComponentProps<typeof AddIntegrationDialog>> = {}) {
  const props = {
    open: true,
    onOpenChange: vi.fn(),
    services: SERVICES,
    sections: SECTIONS,
    onPickService: vi.fn(),
    onPickTools: vi.fn(),
    toolsConfigured: true,
    ...over,
  }
  render(<AddIntegrationDialog {...props} />)
  return props
}

/** Step 1 → step 2. */
function chooseNotifications() {
  fireEvent.click(screen.getByRole("button", { name: /somewhere crewship reaches a person/i }))
}

describe("AddIntegrationDialog", () => {
  beforeEach(() => cleanup())

  it("mounts the shared create surface at the lg width", () => {
    renderDialog()

    const content = document.querySelector('[data-slot="dialog-content"]')
    expect(content).not.toBeNull()
    // The shell's own geometry, not a per-dialog `sm:max-w-2xl`.
    expect(content!.className).toContain("group/surface")
    expect(content!.className).toContain("sm:max-w-[800px]")
    expect(content!.className).not.toContain("sm:max-w-2xl")
  })

  it("opens with the first question focused", () => {
    renderDialog()
    // The shell deliberately does not autofocus, so a surface with no field to
    // type in has to focus its first choice or the dialog opens with focus
    // still on the button that opened it.
    expect(screen.getByRole("button", { name: /somewhere crewship reaches a person/i })).toHaveFocus()
  })

  it("hands the host the chosen notification service, after closing", () => {
    const props = renderDialog()

    chooseNotifications()
    fireEvent.click(screen.getByRole("button", { name: /channels, threads and approvals/i }))

    expect(props.onOpenChange).toHaveBeenCalledWith(false)
    expect(props.onPickService).toHaveBeenCalledTimes(1)
    expect(props.onPickService).toHaveBeenCalledWith(SERVICES[0])
  })

  it("hands tools straight to the host and creates nothing", () => {
    const props = renderDialog()

    fireEvent.click(screen.getByRole("button", { name: /something an agent can act through/i }))

    expect(props.onOpenChange).toHaveBeenCalledWith(false)
    expect(props.onPickTools).toHaveBeenCalledTimes(1)
    expect(props.onPickService).not.toHaveBeenCalled()
    // Tools never reaches a second step — it is a doorway, not a creator.
    expect(screen.queryByLabelText(/search notification services/i)).toBeNull()
  })

  it("says when tools has no API key yet", () => {
    renderDialog({ toolsConfigured: false })
    expect(screen.getByText(/needs an api key/i)).toBeInTheDocument()
  })

  it("filters the catalog and keeps the matched/total count", () => {
    renderDialog()
    chooseNotifications()

    const search = screen.getByLabelText(/search notification services/i)
    expect(screen.getByText("3/3")).toBeInTheDocument()

    fireEvent.change(search, { target: { value: "slack" } })

    expect(screen.getByText("1/3")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /channels, threads and approvals/i })).toBeInTheDocument()
    // A section with nothing left in it does not render its heading either.
    expect(screen.queryByRole("button", { name: /push to a phone/i })).toBeNull()
    expect(screen.queryByRole("heading", { name: /push/i })).toBeNull()

    fireEvent.change(search, { target: { value: "zzz" } })
    expect(screen.getByText(/nothing matches/i)).toBeInTheDocument()
  })

  it("will not hand over a service this instance has switched off", () => {
    const props = renderDialog()
    chooseNotifications()

    const off = screen.getByRole("button", { name: /servers and channels/i })
    expect(off).toBeDisabled()
    fireEvent.click(off)
    expect(props.onPickService).not.toHaveBeenCalled()
    expect(screen.getByText(/dimmed services are switched off/i)).toBeInTheDocument()
  })

  it("goes back to the kind question", () => {
    renderDialog()
    chooseNotifications()
    expect(screen.getByLabelText(/search notification services/i)).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /back/i }))

    expect(screen.queryByLabelText(/search notification services/i)).toBeNull()
    expect(screen.getByRole("button", { name: /somewhere crewship reaches a person/i })).toBeInTheDocument()
  })
})
