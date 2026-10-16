import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "@/lib/slides/model"
import type { SlidesState } from "@/lib/slides/mutations"
import type { SlideTextRange } from "@/lib/slides/selected"
import { SlideSelectedWork } from "../slide-selected-work"
vi.mock("@/hooks/use-slides-selected", () => ({ useSlidesSelected: vi.fn() }))
vi.mock("@/lib/slides/selected", async original => ({ ...await original<typeof import("@/lib/slides/selected")>(), captureSlideTextRange: vi.fn() }))
import { useSlidesSelected } from "@/hooks/use-slides-selected"
import { captureSlideTextRange } from "@/lib/slides/selected"
const hook = vi.mocked(useSlidesSelected), capture = vi.mocked(captureSlideTextRange)
const saved: SlidesState = { schema_version: 1, revision: 1, order_version: 1, content_hash: "a".repeat(64), deck: fixture.deck as SlideDeck }
const range: SlideTextRange = { slide_id: "slide_b", element_id: "title_b", from: 0, to: 1, quote: "P" }
const captured = { base_rev: 1, page_id: saved.deck.deck_id, selection: { ...range, selection_schema_version: 1 as const, kind: "text_range" as const, element_hash: "b".repeat(64) } }
const work = () => ({ page: { items: [], next_cursor: "", watermark: 1, has_more: false, can_dispatch: true, can_cancel: true, eligible_chats: [{ chat_id: "chat_one", agent_name: "Agent", title: "Private chat" }] }, items: [], busy: false, pending: undefined, blocked: false, error: undefined, status: "Ready", refresh: vi.fn(), send: vi.fn().mockResolvedValue(true), discard: vi.fn() })
const props = () => ({ workspaceId: "workspace", slug: "report", pageId: saved.deck.deck_id, saved, range, ready: true, onViewResult: vi.fn().mockResolvedValue(undefined) })
beforeEach(() => { hook.mockReturnValue(work()); capture.mockResolvedValue(captured) })
afterEach(() => { cleanup(); vi.resetAllMocks() })
it("requires an actual nonempty text selection and confirmed clean content", () => {
 const p = props(), h = render(<SlideSelectedWork {...p} range={undefined} />)
 expect(screen.getByRole("button", { name: "Use selected saved text" })).toBeDisabled()
 h.rerender(<SlideSelectedWork {...p} ready={false} />)
 expect(screen.getByRole("button", { name: "Use selected saved text" })).toBeDisabled(); expect(capture).not.toHaveBeenCalled()
})
it("rejects async capture when confirmed revision or readiness changes", async () => {
 let finish!: (value: typeof captured) => void; capture.mockImplementation(() => new Promise(resolve => { finish = resolve }))
 const p = props(), h = render(<SlideSelectedWork {...p} />)
 fireEvent.click(screen.getByRole("button", { name: "Use selected saved text" }))
 h.rerender(<SlideSelectedWork {...p} saved={{ ...saved, revision: 2 }} ready={false} />)
 await act(async () => { finish(captured) })
 expect(screen.queryByLabelText("Selected saved text")).not.toBeInTheDocument(); expect(screen.getByRole("alert")).toHaveTextContent("changed while being captured")
})
it("freezes a real captured selection with original submission identity", async () => {
 const state = work(); hook.mockReturnValue(state); render(<SlideSelectedWork {...props()} />)
 fireEvent.click(screen.getByRole("button", { name: "Use selected saved text" })); await waitFor(() => expect(screen.getByLabelText("Selected saved text")).toBeInTheDocument())
 fireEvent.change(screen.getByLabelText("Slides private agent chat"), { target: { value: "chat_one" } }); fireEvent.change(screen.getByLabelText("Slides agent instruction"), { target: { value: "Improve selected wording" } })
 fireEvent.click(screen.getByRole("button", { name: "Send selected text to agent" })); await waitFor(() => expect(state.send).toHaveBeenCalledTimes(1))
 expect(state.send.mock.calls[0][0]).toMatchObject({ kind: "admit", body: { ...captured, schema_version: 1, type_id: "slides", chat_id: "chat_one", instruction: "Improve selected wording" } })
})
it("shows no creator controls or private chat chooser for a reader", () => {
 const state = work(); state.page.can_dispatch = false; state.page.can_cancel = false; state.page.eligible_chats = []; hook.mockReturnValue(state); render(<SlideSelectedWork {...props()} ready={false} />)
 expect(screen.queryByLabelText("Slides private agent chat")).not.toBeInTheDocument(); expect(screen.queryByRole("button", { name: "Send selected text to agent" })).not.toBeInTheDocument()
})
it("clears server-owned original selection history after authority denial", () => {
 const state = work(), request = { id: "server_request", page_id: saved.deck.deck_id, schema_version: 1 as const, version: 4, status: "running" as const, base_revision: 1, base_schema_version: 1 as const, base_content_hash: "a".repeat(64), selection: captured.selection, instruction: "Private saved instruction", human_actor: "user:owner", agent_actor: "agent:worker", original_run_id: "run_one", result_revision: 2, result_hash: "b".repeat(64), error: "", restored: false, created_at: "2026-10-16T00:00:00Z", updated_at: "2026-10-16T00:00:00Z" }
 const withRequest = { ...state, items: [request] }; hook.mockReturnValue(withRequest); const p = props(), h = render(<SlideSelectedWork {...p} />)
 fireEvent.click(screen.getByRole("button", { name: "View original selection" })); expect(screen.getByLabelText("Original agent selection")).toBeInTheDocument()
 hook.mockReturnValue({ ...state, page: undefined, blocked: true, error: "revoked", items: [] }); h.rerender(<SlideSelectedWork {...p} />)
 expect(screen.queryByLabelText("Original agent selection")).not.toBeInTheDocument(); expect(screen.queryByText("Private saved instruction")).not.toBeInTheDocument()
})
it("lets a clean authorized reader view a confirmed result without admission controls", () => {
 const state = work(); state.page.can_dispatch = false; state.page.can_cancel = false; state.page.eligible_chats = []
 const request = { id: "reader_request", page_id: saved.deck.deck_id, schema_version: 1 as const, version: 5, status: "succeeded" as const, base_revision: 1, base_schema_version: 1 as const, base_content_hash: "a".repeat(64), selection: captured.selection, instruction: "Saved instruction", human_actor: "user:owner", agent_actor: "agent:worker", original_run_id: "run_one", result_revision: 2, result_hash: "b".repeat(64), error: "", restored: false, created_at: "2026-10-16T00:00:00Z", updated_at: "2026-10-16T00:00:00Z" }
 hook.mockReturnValue({ ...state, items: [request] }); const p = props(); render(<SlideSelectedWork {...p} ready={false} canViewResult />)
 fireEvent.click(screen.getByRole("button", { name: "View confirmed result revision 2" })); expect(p.onViewResult).toHaveBeenCalledWith(request); expect(screen.queryByLabelText("Slides private agent chat")).not.toBeInTheDocument()
})
