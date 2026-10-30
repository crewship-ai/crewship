import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import * as Y from "yjs"
import { DesignEditor } from "../design-editor"
import { designNodes } from "../design-document"
const state = vi.hoisted(() => ({ editable: true, doc: null as Y.Doc | null, pageId: "old-private-page", user: false, peerColor: "#2563eb" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => state.user ? { data: { user: { id: "same-user" } } } : null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: () => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: state.pageId, can_edit: state.editable, can_comment: state.editable, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: [] }, isError: false },
  versions: { data: { versions: [{ version: 1 }], shared_version: 1 }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [{ clientId: 2, name: "Marie", color: state.peerColor, cursor: { x: 12, y: 24 } }], connection: "connected", editable: state.editable, dirty: false, draftError: false,
  save: mutation, restore: mutation, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(),
 }
} }))
beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); state.editable = true; state.pageId = "old-private-page"; state.user = false; state.peerColor = "#2563eb"; state.doc = new Y.Doc() })
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs() })
describe("Design editor controls", () => {
 it("clears private unsent feedback when the same slug resolves to a new Page", () => {
  state.user = true
  const view = render(<DesignEditor workspaceId="workspace" slug="same-slug" />)
  fireEvent.change(screen.getByLabelText("Comment for agent"), { target: { value: "Private feedback about old Page" } })
  expect(screen.getByLabelText("Comment for agent")).toHaveValue("Private feedback about old Page")
  state.pageId = "new-shared-page"
  view.rerender(<DesignEditor workspaceId="workspace" slug="same-slug" />)
  expect(screen.getByLabelText("Comment for agent")).toHaveValue("")
 })
 it("creates a screen and keeps awareness visible", () => {
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(screen.getByRole("button", { name: "Screen" }))
  expect(designNodes(state.doc!)).toMatchObject([{ type: "frame", name: "Screen 1", width: 360, height: 640 }])
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
 })
 it("draws peer labels on a fill that keeps white text readable", () => {
  state.peerColor = "#059669"
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByTitle("Marie")).toHaveStyle({ backgroundColor: "#047857" })
  expect(screen.getByLabelText("Marie cursor").querySelector("rect")).toHaveAttribute("fill", "#047857")
 })
 it("does not offer edit or save controls to readers", () => {
  state.editable = false
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByText("Read only")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Screen" })).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Save version" })).not.toBeInTheDocument()
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
 })
})
