import { cleanup, render, screen } from "@testing-library/react"
import { renderToString } from "react-dom/server"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const navigation = vi.hoisted(() => ({ router: { replace: vi.fn() }, redirect: vi.fn((target: string): never => { throw new Error(`REDIRECT ${target}`) }) }))
vi.mock("next/navigation", () => ({
 useRouter: () => navigation.router,
 usePathname: () => window.location.pathname,
 useSearchParams: () => new URLSearchParams(window.location.search),
 redirect: navigation.redirect,
}))
import IssuePage, { generateStaticParams as issueParams } from "./(dashboard)/orchestration/issues/[identifier]/page"
import ProjectPage, { generateStaticParams as projectParams } from "./(dashboard)/orchestration/projects/[projectId]/page"
import MissionPage, { generateStaticParams as missionParams } from "./(dashboard)/orchestration/missions/[missionId]/page"
import OrchestrationPage from "./(dashboard)/orchestration/page"
import WorkPage from "./(dashboard)/work/page"
import InboxPage from "./(dashboard)/inbox-v2/page"
import RunsPage from "./(dashboard)/runs/page"

beforeEach(() => { navigation.router.replace.mockClear(); navigation.redirect.mockClear(); window.history.replaceState({}, "", "/") })
afterEach(() => { cleanup(); window.history.replaceState({}, "", "/") })

describe("bookmarks after navigation migration", () => {
 it.each([
  ["/orchestration/issues/OPS-4", "/issues/OPS-4"],
  ["/orchestration/issues/needs%20review%2Fnow", "/issues/needs%20review%2Fnow"],
  ["/orchestration/issues/50%off", "/issues/50%25off"],
  ["/orchestration/issues/_", "/issues"],
 ])("resolves the actual issue URL %s after static hydration", (path, target) => {
  expect(issueParams()).toEqual([{ identifier: "_" }])
  window.history.replaceState({}, "", path)
  expect(renderToString(<IssuePage />)).toBe("")
  expect(navigation.router.replace).not.toHaveBeenCalled()
  render(<IssuePage />)
  expect(navigation.router.replace).toHaveBeenLastCalledWith(target)
 })
 it("waits for a matching URL and reacts to sibling navigation", () => {
  const view = render(<IssuePage />)
  expect(navigation.router.replace).not.toHaveBeenCalled()
  window.history.replaceState({}, "", "/orchestration/issues/OPS-5")
  view.rerender(<IssuePage />)
  expect(navigation.router.replace).toHaveBeenLastCalledWith("/issues/OPS-5")
  window.history.replaceState({}, "", "/orchestration/issues/OPS-6/")
  view.rerender(<IssuePage />)
  expect(navigation.router.replace).toHaveBeenLastCalledWith("/issues/OPS-6")
 })
 it("keeps project and mission static stubs pointed at replacement surfaces", () => {
  expect(projectParams()).toEqual([{ projectId: "_" }])
  expect(missionParams()).toEqual([{ missionId: "_" }])
  const project = render(<ProjectPage />)
  expect(navigation.router.replace).toHaveBeenLastCalledWith("/issues")
  project.unmount()
  const mission = render(<MissionPage />)
  expect(navigation.router.replace).toHaveBeenLastCalledWith("/activity")
  mission.unmount()
  render(<OrchestrationPage />)
  expect(navigation.router.replace).toHaveBeenLastCalledWith("/activity")
 })
 it.each(["", "?section=deliveries&workspace_id=team%20one", "?section=unknown&source=webhook"])("preserves work filters without a back-button loop: %s", query => {
  window.history.replaceState({}, "", `/work${query}`)
  render(<WorkPage />)
  const params = new URLSearchParams(query)
  params.set("section", params.get("section") === "deliveries" ? "deliveries" : "work")
  const target = `/activity?${params}`
  expect(navigation.router.replace).toHaveBeenCalledWith(target, { scroll: false })
  expect(screen.getByRole("link", { name: "Activity" })).toHaveAttribute("href", target)
 })
 it("preserves inbox filters and selected anchor", () => {
  window.history.replaceState({}, "", "/inbox-v2?workspace_id=team&filter=unread#event-42")
  render(<InboxPage />)
  expect(navigation.router.replace).toHaveBeenCalledWith("/inbox?workspace_id=team&filter=unread#event-42")
  expect(screen.getByRole("link", { name: "Open Inbox" })).toHaveAttribute("href", "/inbox")
 })
 it("redirects retired runs to the journal preset before rendering", () => {
  expect(() => RunsPage()).toThrow("REDIRECT /journal?tab=runs")
  expect(navigation.redirect).toHaveBeenCalledWith("/journal?tab=runs")
 })
})
