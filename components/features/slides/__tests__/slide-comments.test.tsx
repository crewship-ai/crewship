import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { SlideComments } from "../slide-comments"
import { page, thread } from "@/lib/slides/__tests__/comment-fixtures"
import { isNativeSlideDeck } from "../native-model"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlidesState } from "@/lib/slides/mutations"
const mocked = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))
vi.mock("@/hooks/use-slide-comments", () => ({ useSlideComments: () => mocked.value }))
const saved = (): SlidesState => { const deck = structuredClone(fixture.deck); if (!isNativeSlideDeck(deck)) throw new Error("Invalid model fixture"); deck.deck_id = "page_one"; return { schema_version: 1, revision: 2, order_version: 1, content_hash: "a".repeat(64), deck } }

beforeEach(() => { mocked.value = { threads: [thread()], page: { ...page(), can_resolve: true }, history: undefined, busy: false, pending: undefined, blocked: false, status: "Loaded", refresh: vi.fn(), loadHistory: vi.fn(), send: vi.fn().mockResolvedValue(true), discardAttempt: vi.fn() } })
const props = () => ({ workspaceId: "workspace", slug: "report", pageId: "page_one", saved: saved(), contentPending: false })
describe("saved target comment composer", () => {
 it("blocks creating targets in dirty/uncertain content", () => { render(<SlideComments {...props()} contentPending />); expect(screen.getByRole("button", { name: "Comment on saved target" })).toBeDisabled(); expect(screen.getByText(/Save or discard Slides/)).toBeVisible() })
 it("retains reply body/version across refresh, rename and new saved revision", async () => {
  const h = render(<SlideComments {...props()} />); fireEvent.click(screen.getByRole("button", { name: "Reply to version 1" })); fireEvent.change(screen.getByLabelText("Comment body"), { target: { value: "retained literal <script>reply</script>" } })
  mocked.value = { ...mocked.value, threads: [{ ...thread(), version: 5 }] }; h.rerender(<SlideComments {...props()} slug="renamed" saved={{ ...saved(), revision: 7 }} />)
  expect(screen.getByLabelText("Comment body")).toHaveValue("retained literal <script>reply</script>"); fireEvent.click(screen.getByRole("button", { name: "Send reply" })); await waitFor(() => expect(mocked.value.send).toHaveBeenCalledWith(expect.objectContaining({ body: expect.objectContaining({ if_version: 1, body: "retained literal <script>reply</script>" }) })))
 })
 it("prevents resolve from creating an unrelated pending request while a draft exists", () => { render(<SlideComments {...props()} />); fireEvent.click(screen.getByRole("button", { name: "Reply to version 1" })); fireEvent.change(screen.getByLabelText("Comment body"), { target: { value: "unsent reply" } }); expect(screen.getByRole("button", { name: "Resolve thread" })).toBeDisabled(); expect(screen.getByLabelText("Comment body")).toHaveValue("unsent reply") })
 it("disables replies on resolved threads and reports restored provenance", () => { mocked.value = { ...mocked.value, threads: [{ ...thread(), resolved: true, status: "restored", projection_revision: 4, last_missing_revision: 3, restored_at_revision: 4 }] }; render(<SlideComments {...props()} />); expect(screen.getByRole("button", { name: "Reply to version 1" })).toBeDisabled(); expect(screen.getByText(/Restored original target at revision 4/)).toBeVisible() })
})
