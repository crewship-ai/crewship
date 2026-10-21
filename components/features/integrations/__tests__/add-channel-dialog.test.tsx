import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

import { AddChannelDialog } from "../add-channel-dialog"
import type { NotificationProvider } from "@/hooks/use-notification-channels"

// Add channel is three steps — App, Where it posts, What it sends — and Add
// webhook is the same dialog from step two. `create` and `sendDraftTest` come
// from useNotificationChannels; the bodies they get are the contract.

const SLACK: NotificationProvider = {
  provider: "slack",
  label: "Slack",
  blurb: "Post into a Slack channel with an incoming webhook.",
  category: "chat",
  enabled: true,
  fields: [{ key: "webhook_url", label: "Webhook URL", type: "url", required: true }],
}
const OFF: NotificationProvider = { provider: "discord", label: "Discord", blurb: "", category: "chat", enabled: false, fields: [] }

function renderDialog(over: Partial<Parameters<typeof AddChannelDialog>[0]> = {}) {
  const props = {
    mode: "channel" as const,
    onClose: vi.fn(),
    providers: [SLACK, OFF],
    canCreateWorkspace: true,
    create: vi.fn().mockResolvedValue({ id: "ch-1" }),
    sendDraftTest: vi.fn().mockResolvedValue(undefined),
    onCreated: vi.fn(),
    ...over,
  }
  render(<AddChannelDialog {...props} />)
  return props
}
const click = (name: string | RegExp) => fireEvent.click(screen.getByRole("button", { name }))

afterEach(cleanup)

describe("AddChannelDialog", () => {
  it("renders nothing while closed", () => {
    renderDialog({ mode: null })
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("disabled provider not offered", () => {
    renderDialog()
    expect(screen.getByRole("radio", { name: /Slack/ })).toBeInTheDocument()
    expect(screen.queryByRole("radio", { name: /Discord/ })).not.toBeInTheDocument()
    expect(screen.getByRole("radio", { name: /Email/ })).toBeInTheDocument()
  })

  it("walks App → Where it posts → What it sends and posts the provider's fields", async () => {
    const p = renderDialog()
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
    fireEvent.click(screen.getByRole("radio", { name: /Slack/ }))
    click("Continue")
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
    fireEvent.change(screen.getByLabelText(/Webhook URL/), { target: { value: "https://hooks.slack.com/x" } })
    click("Continue")
    fireEvent.click(screen.getByRole("checkbox", { name: "Failed" }))
    fireEvent.click(within(screen.getByRole("group", { name: "Lowest priority" })).getByRole("button", { name: "High" }))
    click("Connect Slack")
    await waitFor(() =>
      expect(p.create).toHaveBeenCalledWith({ type: "shoutrrr", provider: "slack", fields: { webhook_url: "https://hooks.slack.com/x" }, personal: false, categories: ["routines.failed"], min_priority: "high" }),
    )
    expect(p.onCreated).toHaveBeenCalledWith("ch-1", false)
    expect(p.onClose).toHaveBeenCalled()
  })

  it("tests the email draft without saving it", async () => {
    const p = renderDialog()
    fireEvent.click(screen.getByRole("radio", { name: /Email/ }))
    click("Continue")
    fireEvent.change(screen.getByLabelText("Email address"), { target: { value: "ops@example.com" } })
    click("Send test")
    await waitFor(() => expect(p.sendDraftTest).toHaveBeenCalledWith({ type: "email", to: "ops@example.com" }))
    expect(await screen.findByText("Test delivered")).toBeInTheDocument()
    expect(p.create).not.toHaveBeenCalled()
  })

  it("Add webhook starts at step two and goes to Webhooks", () => {
    renderDialog({ mode: "webhook" })
    expect(screen.getByRole("heading", { name: /Add webhook/ })).toBeInTheDocument()
    expect(screen.getByLabelText("URL")).toBeInTheDocument()
  })

  it("secret step keeps footer with Done", async () => {
    const p = renderDialog({ mode: "webhook", create: vi.fn().mockResolvedValue({ id: "w1", secret: "whsec_1" }) })
    fireEvent.change(screen.getByLabelText("URL"), { target: { value: "https://example.com/h" } })
    click("Continue")
    click("Add webhook")
    expect(await screen.findByText("whsec_1")).toBeInTheDocument()
    expect(p.onClose).not.toHaveBeenCalled()
    click("Done")
    expect(p.onClose).toHaveBeenCalled()
    expect(p.create).toHaveBeenCalledWith({ type: "webhook", url: "https://example.com/h", personal: false })
  })

  it("workspace scope locked for MEMBER", async () => {
    const p = renderDialog({ canCreateWorkspace: false })
    fireEvent.click(screen.getByRole("radio", { name: /Email/ }))
    click("Continue")
    const whose = screen.getByRole("group", { name: "Whose" })
    expect(within(whose).getByRole("button", { name: "Workspace" })).toBeDisabled()
    expect(within(whose).getByRole("button", { name: "Only mine" })).toHaveAttribute("aria-pressed", "true")
    fireEvent.change(screen.getByLabelText("Email address"), { target: { value: "me@example.com" } })
    click("Continue")
    expect(screen.queryByRole("checkbox", { name: "Failed" })).not.toBeInTheDocument()
    click("Connect Email")
    await waitFor(() => expect(p.create).toHaveBeenCalledWith({ type: "email", to: "me@example.com", personal: true }))
  })

  it("shows the server's refusal and stays open", async () => {
    const p = renderDialog({ create: vi.fn().mockRejectedValue(new Error("Slack is turned off")) })
    fireEvent.click(screen.getByRole("radio", { name: /Slack/ }))
    click("Continue")
    fireEvent.change(screen.getByLabelText(/Webhook URL/), { target: { value: "https://hooks.slack.com/x" } })
    click("Continue")
    click("Connect Slack")
    expect(await screen.findByText("Slack is turned off")).toBeInTheDocument()
    expect(p.onClose).not.toHaveBeenCalled()
  })
})
