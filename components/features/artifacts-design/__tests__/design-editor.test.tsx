import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import * as Y from "yjs"
import { DesignEditor } from "../design-editor"
import { designNodes, upsertDesignNode } from "../design-document"
const state = vi.hoisted(() => ({ editable: true, doc: null as Y.Doc | null, pageId: "old-private-page", user: false, saveError: null as (Error & { status?: number }) | null, restoreError: null as (Error & { status?: number }) | null }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => state.user ? { data: { user: { id: "same-user" } } } : null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: () => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: state.pageId, can_edit: state.editable, can_comment: state.editable, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: [] }, isError: false },
  versions: { data: { versions: [{ version: 1 }], shared_version: 1 }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [{ clientId: 2, name: "Marie", color: "#2563eb", cursor: { x: 12, y: 24 } }], connection: "connected", editable: state.editable, dirty: false, draftError: false,
  save: { ...mutation, error: state.saveError }, restore: { ...mutation, error: state.restoreError }, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(),
 }
} }))
beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); state.editable = true; state.pageId = "old-private-page"; state.user = false; state.saveError = null; state.restoreError = null; state.doc = new Y.Doc() })
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
 it("does not offer edit or save controls to readers", () => {
  state.editable = false
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  expect(screen.getByText("Read only")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Screen" })).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Save version" })).not.toBeInTheDocument()
  expect(screen.getByLabelText("Marie cursor")).toBeInTheDocument()
 })
})


it.each(["save", "restore"] as const)("offers an actual draft download after %s fills history without discarding the canvas or feedback", async operation => {
 state.user = true
 state[operation === "save" ? "saveError" : "restoreError"] = Object.assign(new Error("Design capacity is full; existing work is retained"), { status: 413 })
 upsertDesignNode(state.doc!, { id: "unsaved", type: "text", text: "Keep my local canvas", x: 0, y: 0, width: 240, height: 40 })
 render(<DesignEditor workspaceId="workspace" slug="shop" initialVersion={operation === "restore" ? 1 : undefined} />)
 if (operation === "restore") {
  expect(screen.getByRole("button", { name: "Download draft" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Return to live canvas" }))
 }
 fireEvent.change(screen.getByLabelText("Comment for agent"), { target: { value: "Keep my pending feedback" } })
 const originalCreate = Object.getOwnPropertyDescriptor(URL, "createObjectURL")
 const originalRevoke = Object.getOwnPropertyDescriptor(URL, "revokeObjectURL")
 const create = vi.fn((_blob: Blob) => "blob:design-draft")
 const revoke = vi.fn()
 const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
 Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create })
 Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revoke })
 try {
  expect(screen.getByRole("button", { name: "Download draft" })).toBeEnabled()
  fireEvent.click(screen.getByRole("button", { name: "Download draft" }))
  expect(create).toHaveBeenCalledOnce()
  expect(click).toHaveBeenCalledOnce()
  const anchor = click.mock.instances[0] as HTMLAnchorElement
  expect(anchor.download).toMatch(/design.*draft.*\.json$/)
  const blob = create.mock.calls[0][0]
  const text = await new Promise<string>((resolve, reject) => {
   const reader = new FileReader()
   reader.onload = () => resolve(String(reader.result))
   reader.onerror = () => reject(reader.error)
   reader.readAsText(blob)
  })
  const downloaded = JSON.parse(text)
  expect(downloaded.nodes).toMatchObject([{ id: "unsaved", text: "Keep my local canvas" }])
  expect(downloaded.feedback_draft.text).toBe("Keep my pending feedback")
  const recovered = new Y.Doc()
  try {
   Y.applyUpdate(recovered, Uint8Array.from(atob(downloaded.yjs_state), char => char.charCodeAt(0)))
   expect(designNodes(recovered)).toEqual(designNodes(state.doc!))
  } finally { recovered.destroy() }
 } finally {
  click.mockRestore()
  if (originalCreate) Object.defineProperty(URL, "createObjectURL", originalCreate); else Reflect.deleteProperty(URL, "createObjectURL")
  if (originalRevoke) Object.defineProperty(URL, "revokeObjectURL", originalRevoke); else Reflect.deleteProperty(URL, "revokeObjectURL")
 }
 expect(screen.getByRole("alert")).toHaveTextContent(/history.*full/i)
 expect(designNodes(state.doc!)).toMatchObject([{ id: "unsaved", text: "Keep my local canvas" }])
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("Keep my pending feedback")
})
