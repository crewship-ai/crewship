import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import * as Y from "yjs"
import { DesignEditor } from "../design-editor"
import { designNodes } from "../design-document"
const state = vi.hoisted(() => ({ editable: true, doc: null as Y.Doc | null }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: () => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: "page", can_edit: state.editable, can_comment: state.editable, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: [] }, isError: false },
  versions: { data: { versions: [{ version: 1 }], shared_version: 1 }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [{ clientId: 2, name: "Marie", color: "#2563eb", cursor: { x: 12, y: 24 } }], connection: "connected", editable: state.editable, dirty: false, draftError: false,
  save: mutation, restore: mutation, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(),
 }
} }))
beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); state.editable = true; state.doc = new Y.Doc() })
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs() })
describe("Design editor controls", () => {
 it("creates a screen and keeps awareness visible", () => {
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(screen.getByRole("button", { name: "Screen", exact: true }))
  expect(designNodes(state.doc!)).toMatchObject([{ type: "frame", name: "Screen 1", width: 360, height: 640 }])
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
 })
 it("does not offer edit or save controls to readers", () => {
  state.editable = false
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByText("Read only")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Screen", exact: true })).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Save version" })).not.toBeInTheDocument()
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
 })
})
