import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ArtifactRequestError } from "../client"
import { query, snapshot } from "./fixtures"
import type { ArtifactToolActivity } from "../tool-result"

const h = vi.hoisted(() => ({
  snap: {} as Record<string, unknown>,
  restore: {} as Record<string, unknown>,
}))
vi.mock("../use-artifact-project-data", () => ({
  useArtifactProjectsAvailable: () => true,
  useArtifactProjectSnapshot: () => h.snap,
  useArtifactProjectMutations: () => ({ restore: h.restore, share: {}, comment: {} }),
}))

import { ArtifactStepCard } from "../artifact-step-card"
import { ArtifactViewerProvider } from "../artifact-viewer-context"

const mutation = (patch: Record<string, unknown> = {}) => ({ mutate: vi.fn(), reset: vi.fn(), isPending: false, error: null, ...patch })
afterEach(() => { cleanup(); h.snap = query({ data: snapshot() }); h.restore = mutation() })
h.snap = query({ data: snapshot() }); h.restore = mutation()

const renderStep = (activity: ArtifactToolActivity) => render(<ArtifactViewerProvider workspaceId="ws-1"><ArtifactStepCard activity={activity} /></ArtifactViewerProvider>)

it("announces a running publish as the next version with an indeterminate progress bar", () => {
  renderStep({ tool: "publish", state: "running", slug: "onboarding", baseVersion: 1 })
  const status = screen.getByRole("status")
  expect(status.textContent).toBe("Creating version 2…")
  expect(status.getAttribute("aria-live")).toBe("polite")
  const bar = screen.getByRole("progressbar", { name: "Creating version 2" })
  expect(bar.hasAttribute("aria-valuenow")).toBe(false)
  expect(screen.queryByRole("button")).toBeNull()
})

it("shows elapsed time only when the start is known", () => {
  vi.useFakeTimers({ now: new Date("2026-10-27T10:00:18Z") })
  try {
    render(<ArtifactViewerProvider workspaceId="ws-1"><ArtifactStepCard activity={{ tool: "publish", state: "running", baseVersion: 1 }} startedAt={new Date("2026-10-27T10:00:00Z")} /></ArtifactViewerProvider>)
    expect(screen.getByText("18s")).toBeTruthy()
  } finally { vi.useRealTimers() }
})

it.each<[ArtifactToolActivity, string]>([
  [{ tool: "create", state: "running" }, "Creating project…"],
  [{ tool: "restore", state: "running", slug: "s", baseVersion: 3, restoreTarget: 1 }, "Restoring v1 as a new version…"],
])("running %o reads %s", (activity, text) => {
  renderStep(activity)
  expect(screen.getByRole("status").textContent).toBe(text)
})

it("done offers Restore of the previous version while it is still the latest", () => {
  renderStep({ tool: "publish", state: "done", slug: "onboarding", version: 2 })
  expect(screen.getByText("v2 ready")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Restore v1" }))
  expect((h.restore.mutate as ReturnType<typeof vi.fn>)).toHaveBeenCalledWith({ base_version: 2, version: 1 })
})

it("done shows the version message as a hint and Open opens the project", () => {
  h.snap = query({ data: snapshot({ versions: [{ version: 2, message: "3 changes · FAQ linked" }] }) })
  renderStep({ tool: "publish", state: "done", slug: "onboarding", version: 2 })
  expect(screen.getByText("3 changes · FAQ linked")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Open" }))
})

it("a finished restore says that nothing was deleted", () => {
  h.snap = query({ data: snapshot({ latest_version: 3 }) })
  h.restore = mutation({ isSuccess: true, variables: { base_version: 2, version: 1 } })
  renderStep({ tool: "publish", state: "done", slug: "onboarding", version: 2 })
  expect(screen.getByRole("status").textContent).toBe("Restored v1 as a new version · nothing deleted")
})

it.each([
  ["a reader", snapshot({ can_write: false })],
  ["a newer version", snapshot({ latest_version: 3 })],
])("offers no restore for %s", (_label, data) => {
  h.snap = query({ data })
  renderStep({ tool: "publish", state: "done", slug: "onboarding", version: 2 })
  expect(screen.getByText("v2 ready")).toBeTruthy()
  expect(screen.queryByRole("button", { name: /Restore/ })).toBeNull()
})

it("offers no restore for the first version", () => {
  renderStep({ tool: "create", state: "done", slug: "onboarding", version: 1 })
  expect(screen.getByText("v1 ready")).toBeTruthy()
  expect(screen.queryByRole("button", { name: /Restore/ })).toBeNull()
})

it("disables Restore while it is pending", () => {
  h.restore = mutation({ isPending: true })
  renderStep({ tool: "publish", state: "done", slug: "onboarding", version: 2 })
  expect((screen.getByRole("button", { name: /Restore v1/ }) as HTMLButtonElement).disabled).toBe(true)
})

it("a conflict offers Reload instead of a stale restore", () => {
  const refetch = vi.fn(() => Promise.resolve())
  h.snap = query({ data: snapshot(), refetch })
  h.restore = mutation({ error: new ArtifactRequestError(409, "artifact_version_conflict", "x") })
  renderStep({ tool: "publish", state: "done", slug: "onboarding", version: 2 })
  expect(screen.getByText(/Changed meanwhile/)).toBeTruthy()
  expect(screen.queryByRole("button", { name: /Restore/ })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Reload" }))
  expect(h.restore.reset).toHaveBeenCalled()
  expect(refetch).toHaveBeenCalled()
})

it("failed shows the error without a retry", () => {
  renderStep({ tool: "publish", state: "failed", slug: "onboarding", error: "quota exceeded" })
  expect(screen.getByText("Couldn’t save version · quota exceeded")).toBeTruthy()
  expect(screen.queryByRole("button")).toBeNull()
})

it("a running new version says the current one stays as it is", () => {
  renderStep({ tool: "publish", state: "running", slug: "onboarding", baseVersion: 3 })
  expect(screen.getByText("You can keep working; v3 stays as it is.")).toBeTruthy()
})

it("a running create has no earlier version to mention", () => {
  renderStep({ tool: "create", state: "running" })
  expect(screen.queryByText(/stays as it is/)).toBeNull()
})

it("a failed new version says in plain words that nothing changed", () => {
  renderStep({ tool: "publish", state: "failed", slug: "onboarding", baseVersion: 2, error: "quota exceeded" })
  expect(screen.getByText("Nothing was saved; v2 is unchanged.")).toBeTruthy()
})

it("a failed create says nothing was saved", () => {
  renderStep({ tool: "create", state: "failed", error: "boom" })
  expect(screen.getByText("Nothing was saved.")).toBeTruthy()
})
