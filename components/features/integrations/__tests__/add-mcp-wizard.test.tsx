import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { AddMCPWizard } from "../add-mcp-wizard"

// =============================================================================
// The Add-MCP wizard's final step used to carry a "Test config" button that was
// a `setTimeout(…, 400)` resolving to a hard-coded "Configuration looks valid."
// It never opened a socket, never read the config it claimed to have checked,
// and could not fail. A user who mistyped an endpoint got a green tick.
//
// Connectivity here needs a persisted server: the only real probes are
// POST /api/v1/{crews/{crewId}/,}integrations/{id}/test, both of which read
// transport/endpoint/command out of the database. Nothing tests a draft, so
// the button was removed rather than wired, and the step now says where the
// real test lives.
//
// These tests pin the removal: the control is gone, and no amount of waiting
// or clicking on step 4 can make the wizard claim a config was verified.
// =============================================================================

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastSuccess = vi.fn()
vi.mock("sonner", () => ({ toast: { success: (...a: unknown[]) => toastSuccess(...a) } }))

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const CREWS = [{ id: "crew-1", name: "Falcon", slug: "falcon" }]

function stubLists() {
  apiFetch.mockImplementation((url: string) => {
    if (url.startsWith("/api/v1/crews?")) return Promise.resolve(json(200, CREWS))
    if (url.startsWith("/api/v1/credentials?")) return Promise.resolve(json(200, []))
    if (url.includes("/integrations")) return Promise.resolve(json(201, { id: "srv-1" }))
    return Promise.resolve(json(200, {}))
  })
}

function renderWizard() {
  const props = {
    workspaceId: "ws-1",
    open: true,
    onOpenChange: vi.fn(),
    onAdded: vi.fn(),
  }
  render(<AddMCPWizard {...props} />)
  return props
}

const cont = () => fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))

/**
 * Press everything the assign step offers except the footer navigation, which
 * would leave the step before a verdict could render. Before the fix this
 * swept up "Test config"; after it, only the crew tiles remain.
 */
function pressEveryStepControl() {
  const nav = /^(cancel|← back|continue|✓ add mcp server|adding\.\.\.|close)$/i
  for (const b of screen.getAllByRole("button")) {
    if (nav.test((b.textContent ?? "").trim())) continue
    fireEvent.click(b)
  }
}

/** Walk source → configure → auth → assign with a valid stdio server. */
async function walkToAssignStep() {
  fireEvent.click(screen.getByRole("button", { name: /custom server/i }))
  cont()
  fireEvent.change(screen.getByPlaceholderText("github"), { target: { value: "github" } })
  fireEvent.change(screen.getByPlaceholderText("npx"), { target: { value: "npx" } })
  cont()
  fireEvent.click(screen.getByRole("button", { name: /no auth required/i }))
  cont()
  await waitFor(() => expect(screen.getByRole("button", { name: /falcon/i })).toBeTruthy())
  fireEvent.click(screen.getByRole("button", { name: /falcon/i }))
}

describe("AddMCPWizard — the fake pre-flight test is gone", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastSuccess.mockReset()
    stubLists()
  })

  it("offers no pre-flight test control on the assign step", async () => {
    renderWizard()
    await walkToAssignStep()

    expect(screen.queryByRole("button", { name: /test config/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /test connection/i })).toBeNull()
  })

  it("cannot be made to claim the configuration was verified", async () => {
    renderWizard()
    await walkToAssignStep()

    // Drain a second of real time before asserting the absence. The old defect
    // surfaced its verdict from a 400ms timer, so an assertion made on the
    // same tick as the click would have passed against it too.
    pressEveryStepControl()
    for (let i = 0; i < 200; i++) await new Promise((r) => setTimeout(r, 5))

    expect(screen.queryByText(/configuration looks valid/i)).toBeNull()
    expect(screen.queryByText(/looks valid/i)).toBeNull()
  })

  it("never issues a connectivity probe from the wizard", async () => {
    renderWizard()
    await walkToAssignStep()

    pressEveryStepControl()
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())

    const probes = apiFetch.mock.calls.filter(([url]) => String(url).includes("/test"))
    expect(probes).toEqual([])
  })

  it("says where the real connectivity test lives instead", async () => {
    renderWizard()
    await walkToAssignStep()

    // The gap has to be visible, not papered over: name the control that does
    // work, and the CLI command that is its contract.
    expect(screen.getByText(/test connection/i)).toBeTruthy()
    expect(
      screen.getByText(/crewship integration crew test <crew-slug> <integration-id>/i),
    ).toBeTruthy()
  })

  it("no longer promises a sanity check in the step description", async () => {
    renderWizard()
    await walkToAssignStep()

    expect(screen.queryByText(/sanity check/i)).toBeNull()
  })

  it("still creates the server when the final action is pressed", async () => {
    const props = renderWizard()
    await walkToAssignStep()

    fireEvent.click(screen.getByRole("button", { name: /add mcp server/i }))

    await waitFor(() => expect(props.onAdded).toHaveBeenCalled())
    const post = apiFetch.mock.calls.find(
      ([url, init]) => String(url).includes("/crews/crew-1/integrations") && init?.method === "POST",
    )
    expect(post).toBeTruthy()
    expect(JSON.parse(post![1].body)).toMatchObject({ name: "github", command: "npx", transport: "stdio" })
  })
})
