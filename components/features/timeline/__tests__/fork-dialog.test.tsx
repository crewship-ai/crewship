import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { ForkDialog } from "../fork-dialog"

// "Fork from here" posted to /api/v1/missions/{id}/fork, which is not a route
// the server has ever registered — the only fork endpoint is
// POST /api/v1/checkpoints/{id}/fork (internal/api/router_orchestration.go).
// Every click 404'd, and the dialog swallowed the 404 as an informational
// "not yet wired" toast, so the button looked like an unfinished feature
// rather than a broken one. These tests pin the request the dialog must
// issue, and the error state a failed fork must leave on screen.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
    info: vi.fn(),
  },
}))

const routerPush = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: routerPush }),
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

function renderDialog(over: Partial<React.ComponentProps<typeof ForkDialog>> = {}) {
  const onOpenChange = vi.fn()
  render(
    <ForkDialog
      open
      onOpenChange={onOpenChange}
      missionId="mis_source"
      checkpointId="cp_abc"
      checkpointLabel="green build"
      {...over}
    />,
  )
  return { onOpenChange }
}

async function submit(label = "experiment-1") {
  if (label) {
    fireEvent.change(screen.getByLabelText(/label/i), { target: { value: label } })
  }
  fireEvent.click(screen.getByRole("button", { name: /^fork$/i }))
}

beforeEach(() => {
  vi.clearAllMocks()
})
afterEach(() => {
  cleanup()
})

describe("ForkDialog", () => {
  it("posts to the checkpoint fork endpoint, not a mission fork endpoint", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ new_mission_id: "mis_fork", new_checkpoint_id: "cp_new" }, 201),
    )
    renderDialog()
    await submit()

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [url, init] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe("/api/v1/checkpoints/cp_abc/fork")
    expect(init.method).toBe("POST")
    expect(JSON.parse(init.body as string)).toEqual({ label: "experiment-1" })
  })

  it("navigates to the forked mission on success", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ new_mission_id: "mis_fork", new_checkpoint_id: "cp_new" }, 201),
    )
    const { onOpenChange } = renderDialog()
    await submit()

    await waitFor(() => expect(routerPush).toHaveBeenCalledWith("/missions/mis_fork/timeline"))
    expect(toastSuccess).toHaveBeenCalled()
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("keeps the dialog open and shows the error when the fork fails", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ error: "checkpoint not found" }, 404))
    const { onOpenChange } = renderDialog()
    await submit()

    // A failed fork must leave evidence on screen — the dialog closing with a
    // toast is what made this button look like it worked.
    expect(await screen.findByRole("alert")).toHaveTextContent(/checkpoint not found/i)
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
    // The typed label survives so the retry doesn't start from an empty box.
    expect(screen.getByLabelText(/label/i)).toHaveValue("experiment-1")
    expect(routerPush).not.toHaveBeenCalled()
  })

  it("reports a transport failure rather than silently closing", async () => {
    apiFetch.mockRejectedValue(new Error("network down"))
    const { onOpenChange } = renderDialog()
    await submit()

    expect(await screen.findByRole("alert")).toBeTruthy()
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })

  it("does not fire a request without a checkpoint id", async () => {
    renderDialog({ checkpointId: null })
    expect(screen.getByRole("button", { name: /^fork$/i })).toBeDisabled()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
