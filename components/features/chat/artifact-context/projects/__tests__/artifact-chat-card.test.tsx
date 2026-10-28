import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"
import { ArtifactRequestError } from "../client"
import { query, snapshot } from "./fixtures"

const h = vi.hoisted(() => ({ result: {} as Record<string, unknown>, calls: [] as unknown[][] }))
vi.mock("../use-artifact-project-data", () => ({
  useArtifactProjectsAvailable: () => true,
  useArtifactProjectSnapshot: (...args: unknown[]) => { h.calls.push(args); return h.result },
}))

import { ArtifactChatCard } from "../artifact-chat-card"
import { ArtifactViewerProvider, useArtifactViewer } from "../artifact-viewer-context"

afterEach(() => { cleanup(); h.calls = []; vi.unstubAllEnvs() })

function Target() {
  const viewer = useArtifactViewer()!
  return <output data-testid="target">{JSON.stringify(viewer.target)}</output>
}
const renderCard = (node: ReactNode) => render(<ArtifactViewerProvider workspaceId="ws-1">{node}<Target /></ArtifactViewerProvider>)

it("loads the live snapshot for the workspace and slug", () => {
  h.result = query({ data: snapshot() })
  renderCard(<ArtifactChatCard slug="onboarding" />)
  expect(h.calls[0]).toEqual(["ws-1", "onboarding"])
})

it("shows a draft project with its kind label and a version badge", () => {
  h.result = query({ data: snapshot() })
  renderCard(<ArtifactChatCard slug="onboarding" fallbackName="ignored" />)
  expect(screen.getByText("Onboarding flow")).toBeTruthy()
  expect(screen.getByText("v2").className).toContain("font-mono")
  expect(screen.getByTestId("artifact-card-kind").textContent).toBe("ProjectDraft")
  expect(screen.getByText("Draft")).toBeTruthy()
  expect(screen.queryByText(/^Shared/)).toBeNull()
})

it("shows Shared when the latest version is the shared one", () => {
  h.result = query({ data: snapshot({ shared_version: 2 }) })
  renderCard(<ArtifactChatCard slug="onboarding" />)
  expect(screen.getByText("Shared")).toBeTruthy()
  expect(screen.queryByText("Draft")).toBeNull()
})

it("shows Draft plus the shared version when a newer draft exists", () => {
  h.result = query({ data: snapshot({ latest_version: 3, shared_version: 2 }) })
  renderCard(<ArtifactChatCard slug="onboarding" />)
  expect(screen.getByText("Draft")).toBeTruthy()
  expect(screen.getByText("Shared v2")).toBeTruthy()
})

it("Open and Comment open the viewer on the right tab and highlight the card", () => {
  h.result = query({ data: snapshot() })
  renderCard(<ArtifactChatCard slug="onboarding" />)
  fireEvent.click(screen.getByRole("button", { name: "Comment on Onboarding flow" }))
  expect(JSON.parse(screen.getByTestId("target").textContent!)).toEqual({ slug: "onboarding", tab: "comments" })
  fireEvent.click(screen.getByRole("button", { name: "Open Onboarding flow" }))
  expect(JSON.parse(screen.getByTestId("target").textContent!)).toEqual({ slug: "onboarding" })
  expect(screen.getByRole("article").getAttribute("data-open")).toBe("true")
  expect(screen.getByRole("article").className).toContain("ring-2 ring-primary")
})

it("rings only the card that opened the viewer when several show the same project", () => {
  h.result = query({ data: snapshot() })
  renderCard(<><ArtifactChatCard slug="onboarding" source="toolu_a" /><ArtifactChatCard slug="onboarding" source="toolu_b" /></>)
  const [first, second] = screen.getAllByRole("article")
  fireEvent.click(within(second).getByRole("button", { name: "Open Onboarding flow" }))
  expect(JSON.parse(screen.getByTestId("target").textContent!)).toEqual({ slug: "onboarding", source: "toolu_b" })
  expect(first.getAttribute("data-open")).toBeNull()
  expect(first.className).not.toContain("ring-2")
  expect(second.getAttribute("data-open")).toBe("true")
  fireEvent.click(within(first).getByRole("button", { name: "Comment on Onboarding flow" }))
  expect(first.getAttribute("data-open")).toBe("true")
  expect(second.getAttribute("data-open")).toBeNull()
})

it.each([403, 404])("a %s shows a restricted card without the name", (status) => {
  h.result = query({ error: new ArtifactRequestError(status, status === 403 ? "forbidden" : "not_found", "x") })
  renderCard(<ArtifactChatCard slug="secret-plan" fallbackName="Secret plan" />)
  expect(screen.getByText("Restricted item · you don’t have access")).toBeTruthy()
  expect(screen.queryByText(/secret/i)).toBeNull()
  expect(screen.queryByRole("button")).toBeNull()
})

it("another error keeps the fallback name and offers Retry", () => {
  const refetch = vi.fn(() => Promise.resolve())
  h.result = query({ error: new ArtifactRequestError(0, "unavailable", "x"), refetch })
  renderCard(<ArtifactChatCard slug="onboarding" fallbackName="Onboarding flow" />)
  expect(screen.getByText("Onboarding flow")).toBeTruthy()
  expect(screen.getByText("Couldn’t load")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(refetch).toHaveBeenCalled()
})

it("shows a skeleton while loading", () => {
  h.result = query({ isPending: true })
  renderCard(<ArtifactChatCard slug="onboarding" />)
  expect(screen.getByLabelText("Loading project").getAttribute("aria-busy")).toBe("true")
})

it("refetches when the step reports a newer version than the cached snapshot", () => {
  const refetch = vi.fn(() => Promise.resolve())
  h.result = query({ data: snapshot({ latest_version: 1 }), refetch })
  renderCard(<ArtifactChatCard slug="onboarding" activity={{ tool: "publish", state: "done", slug: "onboarding", version: 2 }} />)
  expect(refetch).toHaveBeenCalledTimes(1)
})


it.each([
  ["true", "valid", "Dashboard"],
  ["true", "ordinary", "Project"],
  ["false", "valid", "Project"],
  ["true", "invalid", "Project"],
] as const)("labels the actual authorized manifest with Dashboard flag %s and %s files", (flag, kind, label) => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", flag)
  const data = snapshot({ name: "Dashboard from tool text" })
  if (kind !== "ordinary") data.manifest.files.push({ path: "dashboard.json", media_type: "application/json", content: kind === "valid" ? '{"schema_version":1,"collections":["sales"]}' : '{}' })
  h.result = query({ data })
  renderCard(<ArtifactChatCard slug="onboarding" fallbackName="Dashboard" />)
  expect(screen.getByTestId("artifact-card-kind").textContent).toBe(`${label}Draft`)
})
