import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import { EscalationResponseCard } from "../escalation-response-card"
import type { Escalation } from "@/lib/types/escalation"

// Issue #1559 — four-eyes is decided at resolve time from the workspace toggle
// AND the credential's tier, and the row showed neither. An Approve button that
// was going to 403 looked exactly like one that was not, so the refusal was the
// first thing that taught the operator the rule existed. The row now says which
// of the two controls applies, before the click.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

const BASE: Escalation = {
  id: "esc1",
  type: "CREDENTIAL",
  from_name: "Nela",
  from_slug: "nela",
  reason: "need a deploy key",
  context: null,
  metadata: null,
  peer_conversation_id: null,
  status: "PENDING",
  resolution: null,
  action: null,
  redirect_to: null,
  resolved_by: null,
  resolved_at: null,
  created_at: "2026-07-30T10:00:00Z",
  credential_id: "cred1",
  second_approver_required: false,
  second_approver_by_workspace: false,
  second_approver_by_tier: false,
  security_level_label: null,
}

function renderCard(overrides: Partial<Escalation>) {
  render(
    <EscalationResponseCard
      escalation={{ ...BASE, ...overrides }}
      workspaceId="ws1"
      crewId="crew1"
      onResolved={() => {}}
    />,
  )
}

describe("EscalationResponseCard four-eyes notice (#1559)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
  })

  it("says nothing when a single approver can resolve it", () => {
    renderCard({})
    expect(screen.queryByTestId("escalation-four-eyes")).not.toBeInTheDocument()
  })

  it("names the tier when the tier alone forces the rule", () => {
    renderCard({
      second_approver_required: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent("L4 · critical")
    // The bit that is genuinely surprising: the workspace switch is off and the
    // rule applies anyway, and no amount of switching can turn it off.
    expect(note).toHaveTextContent(/second-approver setting is off/i)
    expect(note).toHaveTextContent(/tighten/i)
    // And who is refused — this row is read by the person about to be refused.
    expect(note).toHaveTextContent("@nela")
  })

  it("names the workspace setting when that is what forces the rule", () => {
    renderCard({
      second_approver_required: true,
      second_approver_by_workspace: true,
      security_level_label: "L2 · medium",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent(/workspace/i)
    // The tier is NOT the reason here; claiming it were would send an operator
    // to the credential's level looking for a setting that isn't the cause.
    expect(note).not.toHaveTextContent(/tighten/i)
  })

  it("says both when both hold independently", () => {
    renderCard({
      second_approver_required: true,
      second_approver_by_workspace: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent(/workspace/i)
    expect(note).toHaveTextContent("L4 · critical")
    // Turning the workspace toggle off would not lift it — that is the whole
    // reason both are worth naming rather than picking the "more specific" one.
    expect(note).toHaveTextContent(/regardless/i)
  })
})

// #2376 — the answer to a credential ask is a grant, not a value. The card is
// the one place a human types the secret, and it must go to /supply: never
// through /resolve, which refuses text on a CREDENTIAL escalation.
describe("EscalationResponseCard credential asks (#2376)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ credential: { name: "PG_PASSWORD" } }) })
  })

  it("an ask shows a masked input and posts the value to /supply", async () => {
    const { fireEvent } = await import("@testing-library/react")
    renderCard({ credential_id: "cred1", credential_status: "REQUESTED" })

    expect(screen.getByTestId("escalation-credential-ask")).toBeInTheDocument()
    const input = screen.getByLabelText("Credential value") as HTMLInputElement
    expect(input.type).toBe("password")
    // No name field: the agent's ask already named the credential.
    expect(screen.queryByLabelText("Credential name")).not.toBeInTheDocument()

    fireEvent.change(input, { target: { value: "s3cret" } })
    fireEvent.click(screen.getByRole("button", { name: /supply/i }))

    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [url, init] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toContain("/api/v1/escalations/esc1/supply")
    expect(url).not.toContain("/resolve")
    expect(JSON.parse(String(init.body))).toEqual({ value: "s3cret" })
  })

  it("a free-text ask needs a name and sends it alongside the value", async () => {
    const { fireEvent } = await import("@testing-library/react")
    renderCard({ credential_id: null, credential_status: null })

    const supply = screen.getByRole("button", { name: /supply/i })
    fireEvent.change(screen.getByLabelText("Credential value"), { target: { value: "tok" } })
    expect(supply).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Credential name"), { target: { value: "gh_token" } })
    expect(supply).toBeEnabled()
    fireEvent.click(supply)

    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [, init] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(String(init.body))).toEqual({ value: "tok", name: "GH_TOKEN" })
  })

  it("a proposal has nothing to type and approves through /resolve with no text", async () => {
    const { fireEvent } = await import("@testing-library/react")
    renderCard({ credential_id: "cred1", credential_status: "PENDING_APPROVAL" })

    expect(screen.getByTestId("escalation-credential-proposal")).toBeInTheDocument()
    expect(screen.queryByLabelText("Credential value")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /approve/i }))

    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [url, init] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toContain("/resolve")
    const body = JSON.parse(String(init.body))
    expect(body.action).toBe("approve")
    expect(body.resolution).toBe("")
  })

  it("rejecting a credential ask needs no text and sends none", async () => {
    const { fireEvent } = await import("@testing-library/react")
    renderCard({ credential_id: "cred1", credential_status: "REQUESTED" })

    const reject = screen.getByRole("button", { name: /reject/i })
    expect(reject).toBeEnabled()
    fireEvent.click(reject)

    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [url, init] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toContain("/resolve")
    expect(JSON.parse(String(init.body))).toMatchObject({ action: "reject", resolution: "" })
  })
})
