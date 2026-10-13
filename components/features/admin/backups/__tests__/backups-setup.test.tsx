// Set up backups: a server with no backup key cannot back anything up, so
// Crewship leads the admin through it — a key made in the browser (the
// private half never sent), the plan every server needs, the first backup.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "u1", name: "Pavel Srba" } }, status: "authenticated" }) }))
vi.mock("sonner", () => ({ toast: h.toast }))

import { BackupsSetup, SetupCard, setupState } from "../backups-setup"

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
const posts = (path: string) => h.apiFetch.mock.calls
  .filter(([u, init]) => String(u).endsWith(path) && (init as RequestInit | undefined)?.method === "POST")
  .map(([, init]) => JSON.parse(String((init as RequestInit).body)))

beforeEach(() => {
  cleanup()
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) => {
    const url = String(u)
    if (url.endsWith("/recipients")) return json({ id: "rcp-1", name: "Backup key", public_key: "age1x" })
    if (url.endsWith("/plans")) return json({ id: "plan-1", name: "Complete recovery" })
    if (url.endsWith("/run")) return json({ id: "run-1", run_id: "run-1", run_ids: ["run-1"], status: "queued" })
    return json({})
  })
})

const INSTANCE = { id: "p-all", enabled: true, scope: "instance", preset: "complete", workspace_ids: [] }

describe("setupState", () => {
  it("knows nothing until both were read, and is done with a key and an enabled plan", () => {
    expect(setupState(null, []).known).toBe(false)
    expect(setupState([], [])).toMatchObject({ known: true, hasKey: false, hasPlan: false, done: false })
    expect(setupState([{ id: "k" } as never], [{ ...INSTANCE, enabled: false } as never])).toMatchObject({ hasKey: true, hasPlan: false, done: false })
    expect(setupState([{ id: "k" } as never], [INSTANCE as never]).done).toBe(true)
  })

  it("counts only a plan that brings the whole instance back", () => {
    // A run with a plan_id takes the plan's scope: a plan for one workspace,
    // or for part of the data, does not back up the server.
    expect(setupState([{ id: "k" } as never], [{ ...INSTANCE, scope: "workspaces", preset: "workspace", workspace_ids: ["ws1"] } as never]).hasPlan).toBe(false)
    expect(setupState([{ id: "k" } as never], [{ ...INSTANCE, preset: "custom" } as never]).hasPlan).toBe(false)
  })
})

describe("Set up backups", () => {
  it("lists the three steps on the Overview and starts from there", () => {
    const start = vi.fn()
    render(<SetupCard hasKey={false} hasPlan={false} onStart={start} />)
    expect(document.querySelectorAll("[data-step-done=false]")).toHaveLength(3)
    fireEvent.click(screen.getByRole("button", { name: "Set up backups" }))
    expect(start).toHaveBeenCalled()
  })

  it("makes a key here and sends only its public half, then the plan, then the first backup", async () => {
    const changed = vi.fn()
    render(<BackupsSetup open onOpenChange={() => {}} keys={[]} plans={[]} demo={false} onChanged={changed} onOpenRuns={() => {}} />)
    const dialog = screen.getByRole("dialog")
    fireEvent.click(within(dialog).getByRole("button", { name: /Make the key/ }))
    const priv = await waitFor(() => { const el = dialog.querySelector("[data-slot=private-key]"); if (!el) throw new Error("no key"); return el })
    expect(priv.textContent).toMatch(/^AGE-SECRET-KEY-1/)
    const save = within(dialog).getByRole("button", { name: /Save the key/ })
    // Not before the admin says the private key is kept somewhere safe.
    expect(save).toBeDisabled()
    fireEvent.click(within(dialog).getByRole("checkbox"))
    fireEvent.click(save)

    await waitFor(() => expect(posts("/recipients")).toHaveLength(1))
    const sent = posts("/recipients")[0]
    expect(sent.public_key).toMatch(/^age1/)
    expect(sent.holder).toBe("Pavel Srba")
    // The private key never leaves the browser.
    expect(JSON.stringify(h.apiFetch.mock.calls)).not.toContain(priv.textContent!)

    fireEvent.click(await within(dialog).findByRole("button", { name: "Create the plan" }))
    await waitFor(() => expect(posts("/plans")).toHaveLength(1))
    expect(posts("/plans")[0]).toMatchObject({ preset: "complete", scope: "instance", time_of_day: "03:00", recipient_ids: ["rcp-1"], enabled: true })

    fireEvent.click(await within(dialog).findByRole("button", { name: "Back up now" }))
    await waitFor(() => expect(posts("/run")).toHaveLength(1))
    expect(posts("/run")[0]).toMatchObject({ plan_id: "plan-1", scope: "instance" })
    expect(await within(dialog).findByText(/Backups are set up/)).toBeInTheDocument()
    expect(changed).toHaveBeenCalledTimes(2)
  })

  it("takes a pasted public key, and skips what is already there", async () => {
    const ws = { id: "p-ws", enabled: true, scope: "workspaces", preset: "workspace", workspace_ids: ["ws1"] }
    render(<BackupsSetup open onOpenChange={() => {}} keys={[]} plans={[ws, INSTANCE] as never} demo={false} onChanged={() => {}} onOpenRuns={() => {}} />)
    const dialog = screen.getByRole("dialog")
    fireEvent.click(within(dialog).getByRole("radio", { name: /I have one/ }))
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Public key" }), { target: { value: "age1zvkyg2lqzraa2lnjvqej32nkuu0ues2s82hzrye869xeexvn73equnujwj" } })
    fireEvent.click(within(dialog).getByRole("button", { name: /Save the key/ }))
    // A plan for the whole instance exists: straight to the first backup, run
    // with that plan — not the workspace's plan listed before it.
    fireEvent.click(await within(dialog).findByRole("button", { name: "Back up now" }))
    await waitFor(() => expect(posts("/run")).toHaveLength(1))
    expect(posts("/run")[0]).toMatchObject({ plan_id: "p-all" })
  })
})
