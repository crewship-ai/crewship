import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ViewerLinkSwitcher } from "../viewer-link-switcher"
import { switchableLinks, type ConversationLink, type LinkedViewerTarget } from "../linked-viewer-targets"

afterEach(cleanup)
const t = (pageId: string, revision?: number): LinkedViewerTarget => revision === undefined ? { workspaceId: "w", pageId, mode: "latest" } : { workspaceId: "w", pageId, mode: "revision", revision }
const links: ConversationLink[] = [
  { target: t("a", 1), title: "Plan", accessible: true },
  { target: t("a"), title: "Plan", accessible: true },
  { target: t("b"), title: null, accessible: false },
  { target: t("c", -1), title: "Bad", accessible: true },
  { target: t("a", 1), title: "Plan", accessible: true },
]
it("lists only accessible, valid, unique links", () => {
  expect(switchableLinks(links).map(link => link.target)).toEqual([t("a", 1), t("a")])
})
it("labels an exact revision and requires an explicit switch to latest", () => {
  const onViewLatest = vi.fn(), onSwitch = vi.fn()
  render(<ViewerLinkSwitcher links={links} current={t("a", 1)} onSwitch={onSwitch} onViewLatest={onViewLatest} />)
  expect(screen.getByTestId("viewer-target-label").textContent).toBe("Revision 1")
  expect(onViewLatest).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "View latest" }))
  expect(onViewLatest).toHaveBeenCalledOnce()
  fireEvent.change(screen.getByLabelText("Linked items"), { target: { value: "w/a@latest" } })
  expect(onSwitch).toHaveBeenCalledWith(t("a"))
})
it("never offers an inaccessible link and hides the switch with a single item", () => {
  render(<ViewerLinkSwitcher links={[links[2], links[0]]} current={t("a", 1)} onSwitch={() => {}} />)
  expect(screen.queryByLabelText("Linked items")).toBeNull()
  expect(screen.queryByRole("button", { name: "View latest" })).toBeNull()
})
