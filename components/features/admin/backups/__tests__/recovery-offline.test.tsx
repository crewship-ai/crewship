import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

// Review B6: an Empty server or Isolated instance restore runs offline, where
// the key is. The wizard must reach those instructions with the routes the
// server really has — restore/checks exists, POST /admin/instance/backups/restore
// does not — and never call the missing route.

const h = vi.hoisted(() => ({ api: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn(), info: vi.fn() } }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("../use-backup-runs", () => ({ useBackupRuns: () => ({ data: [], status: "ready", source: "runs", reload: vi.fn() }), workspaceFor: () => null }))
vi.mock("../use-backup-settings", () => ({
  useVaultKeys: () => ({ data: { recovery_kit: { enabled: false }, versions: [] } }),
  useBackupSettings: () => ({ status: "unavailable", data: null }),
  saveBackupSettings: vi.fn(),
}))
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }))
import { RestoreWizard } from "../backups-recovery"
import { checksFixture } from "../__fixtures__/backups"
import type { SectionCtx } from "../backups-console"

const ctx: SectionCtx = {
  scope: "instance", selected: new Set(), workspaces: [], currentWorkspaceId: "a", demo: false, go: vi.fn(),
  focusRun: null, focusPath: "/srv/backups/review 1.tar.zst", backUpNow: vi.fn(), newPlanSignal: 0,
}
const writeText = vi.fn()

beforeEach(() => {
  h.api.mockReset()
  writeText.mockReset().mockResolvedValue(undefined)
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
  // Only the routes the server registers answer; everything else is a 404.
  h.api.mockImplementation((url: string) => Promise.resolve(url.endsWith("/restore/checks")
    ? new Response(JSON.stringify(checksFixture("instance")), { status: 200 })
    : new Response("404 page not found", { status: 404 })))
})
afterEach(cleanup)

function throughChecks(target?: RegExp) {
  render(<RestoreWizard ctx={ctx} />)
  if (target) fireEvent.click(screen.getByRole("radio", { name: target }))
  fireEvent.click(screen.getByRole("button", { name: "Next", exact: true }))
  fireEvent.change(screen.getByLabelText("AGE identity"), { target: { value: "review-only identity" } })
  fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
}
const calledRestore = () => h.api.mock.calls.some(([url]) => /\/admin\/instance\/backups\/restore(\?|$)/.test(String(url)))

// Ported from the reviewer's repro (review-recovery-wizard.test.tsx), minus
// its click on "Run the dry run": an instance target has no online dry run.
it("the instance recovery wizard reaches its CLI instructions with the deployed routes", async () => {
  throughChecks()
  expect(await screen.findByText("Restore from the command line")).toBeInTheDocument()
  expect(screen.getByText("Before anything changes")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Run the dry run" })).toBeNull()
  expect(calledRestore()).toBe(false)
})

it("Empty server shows the exact recover command, and Copy copies it", async () => {
  throughChecks()
  const cmd = "crewship recover --bundle '/srv/backups/review 1.tar.zst' --identity <key-file> --data-dir /var/lib/crewship"
  expect(await screen.findByText(cmd)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Copy" }))
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(cmd))
})

it("Isolated instance shows the drill command that posts its result", async () => {
  throughChecks(/Isolated instance/)
  expect(await screen.findByText("Run the drill from the command line")).toBeInTheDocument()
  const cmd = "crewship backup drill --bundle '/srv/backups/review 1.tar.zst' --identity <key-file> --post"
  expect(screen.getByText(cmd)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Copy" }))
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(cmd))
  expect(calledRestore()).toBe(false)
})

it("checks the server cannot run still lead to the instructions", async () => {
  h.api.mockImplementation(() => Promise.resolve(new Response("404 page not found", { status: 404 })))
  throughChecks()
  expect(await screen.findByText("Restore from the command line")).toBeInTheDocument()
  expect(calledRestore()).toBe(false)
})
