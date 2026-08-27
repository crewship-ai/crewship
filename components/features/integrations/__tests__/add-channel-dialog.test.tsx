import { describe, it, expect, vi, beforeEach } from "vitest"
import type { ComponentProps } from "react"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { AddChannelDialog, type AddChannelTarget } from "../add-channel-dialog"
import type { NotificationProvider } from "@/hooks/use-notification-channels"

// =============================================================================
// "Connect <service>" on the shared create shell.
//
// This is the step that actually creates the channel — the one the catalog
// (add-integration-dialog) hands off to. The catalog was migrated onto
// CreateSurface and this was not, so picking Slack dropped you out of the
// unified surface into an old-style Radix dialog with its own width, its own
// footer buttons and its own header sizes.
//
// The dialog never calls fetch itself: `create` and `sendDraftTest` come from
// useNotificationChannels, which owns POST /api/v1/notification-channels and
// POST /api/v1/notification-channels/test. So "issues the same request" here
// means "calls those two with byte-identical bodies", which is the same
// contract the sibling test asserts for the catalog's hand-off.
// =============================================================================

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

import { toast } from "sonner"

const SLACK: NotificationProvider = {
  provider: "slack",
  label: "Slack",
  blurb: "Post into a Slack channel with an incoming webhook.",
  category: "chat",
  enabled: true,
  fields: [
    {
      key: "webhook_url",
      label: "Webhook URL",
      type: "url",
      required: true,
      placeholder: "https://hooks.slack.com/services/…",
      help: "Slack → Apps → Incoming Webhooks.",
      help_url: "https://api.slack.com/messaging/webhooks",
    },
    {
      key: "bot_name",
      label: "Bot display name",
      type: "text",
      required: false,
      placeholder: "Crewship",
    },
  ],
}

const SHOUTRRR_TARGET: AddChannelTarget = { kind: "shoutrrr", provider: "slack", label: "Slack" }
const WEBHOOK_TARGET: AddChannelTarget = { kind: "webhook", label: "Webhook" }
const EMAIL_TARGET: AddChannelTarget = { kind: "email", label: "Email" }

function renderDialog(over: Partial<ComponentProps<typeof AddChannelDialog>> = {}) {
  const props = {
    target: SHOUTRRR_TARGET,
    onClose: vi.fn(),
    providers: [SLACK],
    canCreateWorkspace: true,
    create: vi.fn().mockResolvedValue({ id: "ch-1" }),
    sendDraftTest: vi.fn().mockResolvedValue(undefined),
    ...over,
  }
  render(<AddChannelDialog {...props} />)
  return props
}

const connect = () => fireEvent.click(screen.getByRole("button", { name: /^connect$/i }))

describe("AddChannelDialog", () => {
  beforeEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  // ── The shell ────────────────────────────────────────────────────────────
  it("mounts the shared create surface at the md width", () => {
    renderDialog()

    const content = document.querySelector('[data-slot="dialog-content"]')
    expect(content).not.toBeNull()
    expect(content!.className).toContain("group/surface")
    expect(content!.className).toContain("sm:max-w-[640px]")
    // The old per-dialog geometry, which is what made this step read as a
    // different product from the step before it.
    expect(content!.className).not.toContain("sm:max-w-lg")
    expect(content!.className).not.toContain("max-h-[85vh]")
  })

  it("renders nothing until a service has been picked", () => {
    renderDialog({ target: null })
    expect(document.querySelector('[data-slot="dialog-content"]')).toBeNull()
  })

  it("keeps the service name and its blurb in the header", () => {
    renderDialog()
    expect(screen.getByText(/connect slack/i)).toBeInTheDocument()
    expect(screen.getByText(SLACK.blurb)).toBeInTheDocument()
  })

  // ── The fields, and the body they produce ────────────────────────────────
  it("posts the provider's fields unchanged", async () => {
    const props = renderDialog()

    fireEvent.change(screen.getByLabelText(/webhook url/i), {
      target: { value: "https://hooks.slack.com/services/T/B/x" },
    })
    fireEvent.change(screen.getByLabelText(/bot display name/i), {
      target: { value: "Crewship" },
    })
    connect()

    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create).toHaveBeenCalledWith({
      type: "shoutrrr",
      provider: "slack",
      fields: { webhook_url: "https://hooks.slack.com/services/T/B/x", bot_name: "Crewship" },
      personal: false,
    })
    expect(toast.success).toHaveBeenCalledWith("Slack connected")
    expect(props.onClose).toHaveBeenCalled()
  })

  it("will not submit until every required field is filled", () => {
    renderDialog()
    expect(screen.getByRole("button", { name: /^connect$/i })).toBeDisabled()

    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })
    expect(screen.getByRole("button", { name: /^connect$/i })).toBeEnabled()
  })

  it("posts a webhook with its endpoint and secret, then reveals the secret once", async () => {
    const props = renderDialog({
      target: WEBHOOK_TARGET,
      create: vi.fn().mockResolvedValue({ id: "ch-2", secret: "s3cr3t" }),
    })

    fireEvent.change(screen.getByLabelText(/endpoint url/i), {
      target: { value: "https://example.com/hooks/crewship" },
    })
    fireEvent.change(screen.getByLabelText(/signing secret/i), { target: { value: "mine" } })
    connect()

    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create).toHaveBeenCalledWith({
      type: "webhook",
      url: "https://example.com/hooks/crewship",
      secret: "mine",
      personal: false,
    })

    // The one-time reveal replaces the form and takes the footer with it.
    expect(await screen.findByText("s3cr3t")).toBeInTheDocument()
    expect(props.onClose).not.toHaveBeenCalled()
    expect(screen.queryByRole("button", { name: /^connect$/i })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: /done/i }))
    expect(props.onClose).toHaveBeenCalled()
  })

  it("omits the webhook secret when it is left blank", async () => {
    const props = renderDialog({ target: WEBHOOK_TARGET })

    fireEvent.change(screen.getByLabelText(/endpoint url/i), {
      target: { value: "https://example.com/hooks/crewship" },
    })
    connect()

    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create).toHaveBeenCalledWith({
      type: "webhook",
      url: "https://example.com/hooks/crewship",
      personal: false,
    })
  })

  it("posts an email address as `to`", async () => {
    const props = renderDialog({ target: EMAIL_TARGET })

    fireEvent.change(screen.getByLabelText(/email address/i), {
      target: { value: "ops@example.com" },
    })
    connect()

    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create).toHaveBeenCalledWith({
      type: "email",
      to: "ops@example.com",
      personal: false,
    })
  })

  // ── Personal / workspace ─────────────────────────────────────────────────
  it("names the personal-connection checkbox, and hides the routing rules when it is on", async () => {
    const props = renderDialog()
    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })

    const personal = screen.getByRole("checkbox", { name: /personal connection/i })
    expect(personal).not.toBeChecked()
    expect(screen.getByRole("checkbox", { name: /^completed$/i })).toBeInTheDocument()

    fireEvent.click(personal)

    // An admin allowlist is meaningless on a channel only its owner routes to.
    expect(screen.queryByRole("checkbox", { name: /^completed$/i })).toBeNull()
    expect(screen.queryByText(/priority floor/i)).toBeNull()

    connect()
    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create).toHaveBeenCalledWith({
      type: "shoutrrr",
      provider: "slack",
      fields: { webhook_url: "https://x" },
      personal: true,
    })
  })

  it("forces personal when the visitor may not create workspace connections", async () => {
    const props = renderDialog({ canCreateWorkspace: false })
    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })

    const personal = screen.getByRole("checkbox", { name: /personal connection/i })
    expect(personal).toBeChecked()
    expect(personal).toBeDisabled()
    expect(screen.getByText(/need the admin or owner role/i)).toBeInTheDocument()

    connect()
    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create).toHaveBeenCalledWith(expect.objectContaining({ personal: true }))
  })

  // ── The categories matrix ────────────────────────────────────────────────
  it("gives every category checkbox a real accessible name", () => {
    renderDialog()
    // A bare Checkbox with its label rendered as a sibling <div> has no
    // accessible name at all, so a screen-reader user hears "checkbox" 18
    // times. Each cell is named by its own label element.
    for (const name of ["Completed", "Failed", "Approval needed", "Stale pages", "Memory"]) {
      expect(screen.getByRole("checkbox", { name })).toBeInTheDocument()
    }
    expect(screen.getByRole("heading", { name: /routines/i })).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: /chat & memory/i })).toBeInTheDocument()

    // The name comes from a real association, not from a happy accident of
    // where the text sits: clicking the label has to tick the cell.
    fireEvent.click(screen.getByText("Escalation"))
    expect(screen.getByRole("checkbox", { name: "Escalation" })).toBeChecked()
  })

  it("sends the ticked categories, and nothing when none are ticked", async () => {
    const props = renderDialog()
    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })

    fireEvent.click(screen.getByRole("checkbox", { name: "Failed" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Approval needed" }))
    connect()

    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create).toHaveBeenCalledWith({
      type: "shoutrrr",
      provider: "slack",
      fields: { webhook_url: "https://x" },
      personal: false,
      categories: ["routines.failed", "agents.approval"],
    })
  })

  it("drops the allowlist when the connection turns personal", async () => {
    const props = renderDialog()
    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })

    fireEvent.click(screen.getByRole("checkbox", { name: "Failed" }))
    fireEvent.click(screen.getByRole("checkbox", { name: /personal connection/i }))
    fireEvent.click(screen.getByRole("checkbox", { name: /personal connection/i }))

    expect(screen.getByRole("checkbox", { name: "Failed" })).not.toBeChecked()

    connect()
    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create.mock.calls[0][0]).not.toHaveProperty("categories")
  })

  it("keeps the priority floor, and omits it at the default", async () => {
    const props = renderDialog()
    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })

    expect(screen.getByRole("combobox", { name: /priority floor/i })).toBeInTheDocument()

    connect()
    await waitFor(() => expect(props.create).toHaveBeenCalledTimes(1))
    expect(props.create.mock.calls[0][0]).not.toHaveProperty("min_priority")
  })

  // ── Send test ────────────────────────────────────────────────────────────
  it("keeps Send test in the body, testing the draft without saving it", async () => {
    const props = renderDialog()

    expect(screen.getByRole("button", { name: /send test/i })).toBeDisabled()
    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })

    fireEvent.click(screen.getByRole("button", { name: /send test/i }))

    await waitFor(() => expect(props.sendDraftTest).toHaveBeenCalledTimes(1))
    expect(props.sendDraftTest).toHaveBeenCalledWith({
      type: "shoutrrr",
      provider: "slack",
      fields: { webhook_url: "https://x" },
    })
    expect(await screen.findByText(/test notification sent/i)).toBeInTheDocument()
    // Testing a draft is not creating one.
    expect(props.create).not.toHaveBeenCalled()
  })

  // ── The refusal ──────────────────────────────────────────────────────────
  it("shows the server's refusal in the band as well as the toast", async () => {
    const props = renderDialog({
      create: vi.fn().mockRejectedValue(new Error("webhook url is not reachable")),
    })
    fireEvent.change(screen.getByLabelText(/webhook url/i), { target: { value: "https://x" } })

    connect()

    // The band survives; the toast is what fades.
    expect(await screen.findByRole("alert")).toHaveTextContent(/webhook url is not reachable/i)
    expect(toast.error).toHaveBeenCalledWith("webhook url is not reachable")
    expect(props.onClose).not.toHaveBeenCalled()
    // Still on the form, still submittable.
    expect(screen.getByRole("button", { name: /^connect$/i })).toBeEnabled()
  })
})
