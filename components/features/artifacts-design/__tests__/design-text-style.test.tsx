vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import * as Y from "yjs"
import { DesignNodeShape } from "../design-node-shape"
import { DesignEditor } from "../design-editor"
import { designNodes, designTextStyle, upsertDesignNode, type DesignNode } from "../design-document"

const state = vi.hoisted(() => ({ doc: null as Y.Doc | null, editable: true }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: () => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: "page", can_edit: state.editable, can_comment: true, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: [] }, isError: false }, versions: { data: { versions: [{ version: 1 }] }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [], connection: "connected", editable: state.editable, dirty: false, unsynced: false, draftError: false,
  save: mutation, restore: mutation, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(), conflicts: [], dismissConflict: vi.fn(),
 }
} }))

const text = (extra: Partial<DesignNode> = {}): DesignNode => ({ id: "t", type: "text", x: 0, y: 0, width: 200, height: 60, fill: "#172554", text: "Line one\nLine two", ...extra })
const draw = (node: DesignNode) => render(<svg><DesignNodeShape node={node} width={node.width} height={node.height} /></svg>).container.querySelector("svg")!

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_TEXT_STYLE", "true"); state.editable = true; state.doc = new Y.Doc() })
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs() })

describe("Design text style", () => {
 it("draws a text layer without style exactly as before text style existed", () => {
  // The markup of main before #5161, attribute for attribute.
  expect(draw(text()).innerHTML).toBe('<rect width="200" height="60" fill="transparent"></rect><text x="0" y="22" fill="#172554" font-size="20" font-family="system-ui, sans-serif"><tspan x="0" dy="0">Line one</tspan><tspan x="0" dy="1.3em">Line two</tspan></text>')
  // Explicit defaults for wrap and overflow are the same look.
  expect(draw(text({ textWrap: "none", overflow: "visible" })).innerHTML).toBe(draw(text()).innerHTML)
 })

 it("draws font family, weight, size, line height and alignment as lines", () => {
  const svg = draw(text({ fontFamily: "serif", fontWeight: 700, fontSize: 40, lineHeight: 1.5, textAlign: "center" }))
  const label = svg.querySelector("text")!
  expect(label).toHaveAttribute("font-family", "Georgia, 'Times New Roman', serif")
  expect(label).toHaveAttribute("font-weight", "700")
  expect(label).toHaveAttribute("font-size", "40")
  expect(label).toHaveAttribute("y", "44")
  expect(label).toHaveAttribute("text-anchor", "middle")
  expect([...label.querySelectorAll("tspan")].map(span => [span.getAttribute("x"), span.getAttribute("dy")])).toEqual([["100", "0"], ["100", "1.5em"]])
 })

 it("wraps inside the box width and clips to the box when asked", () => {
  const wrapped = draw(text({ textWrap: "wrap", text: "A long sentence with a verylongunbreakableword", fontFamily: "mono", fontSize: 16, lineHeight: 1.2, textAlign: "right" }))
  const object = wrapped.querySelector("foreignObject")!
  expect(object).toHaveAttribute("data-text-layout", "wrap")
  const box = object.firstElementChild as HTMLElement
  expect(box.textContent).toBe("A long sentence with a verylongunbreakableword")
  expect(box.style.whiteSpace).toBe("pre-wrap")
  expect(box.style.overflowWrap).toBe("anywhere")
  expect(box.style.textAlign).toBe("right")
  expect(box.style.width).toBe("200px")
  expect(box.style.overflow).toBe("visible")
  const clipped = draw(text({ textWrap: "wrap", overflow: "clip" })).querySelector("foreignObject")!
  expect(clipped).toHaveAttribute("overflow", "hidden")
  expect((clipped.firstElementChild as HTMLElement).style.overflow).toBe("hidden")
  const lines = draw(text({ overflow: "clip" }))
  const clipId = lines.querySelector("clipPath")!.id
  expect(lines.querySelector("text")).toHaveAttribute("clip-path", `url(#${clipId})`)
 })

 it("reads unknown or out-of-range values as the default instead of drawing them", () => {
  const style = designTextStyle({ fontFamily: "Comic Sans" as never, fontWeight: 450, fontSize: 0, lineHeight: 99, textAlign: "justify" as never })
  expect(style).toMatchObject({ family: "sans", weight: 400, size: 20, lineHeight: 1.3, align: "left", wrap: false, clip: false })
 })

 it("edits text style in Properties and keeps it when other properties change", () => {
  upsertDesignNode(state.doc!, { id: "s", type: "frame", name: "Screen", x: 0, y: 0, width: 360, height: 640, fill: "#ffffff" })
  upsertDesignNode(state.doc!, { ...text(), parentId: "s", name: "Headline" })
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Headline" }))
  const style = screen.getByRole("group", { name: "Text style" })
  expect(within(style).getByLabelText("Font size")).toHaveValue(20)
  fireEvent.change(within(style).getByLabelText("Font family"), { target: { value: "serif" } })
  fireEvent.change(within(style).getByLabelText("Font weight"), { target: { value: "700" } })
  fireEvent.change(within(style).getByLabelText("Font size"), { target: { value: "36" } })
  fireEvent.blur(within(style).getByLabelText("Font size"))
  fireEvent.change(within(style).getByLabelText("Line height"), { target: { value: "1.1" } })
  fireEvent.blur(within(style).getByLabelText("Line height"))
  fireEvent.change(within(style).getByLabelText("Text alignment"), { target: { value: "center" } })
  fireEvent.click(within(style).getByLabelText("Wrap text to the box width"))
  fireEvent.click(within(style).getByLabelText("Clip text to the box"))
  // Out of range: nothing is written.
  fireEvent.change(within(style).getByLabelText("Font size"), { target: { value: "5000" } })
  fireEvent.blur(within(style).getByLabelText("Font size"))
  fireEvent.change(within(style).getByLabelText("Line height"), { target: { value: "" } })
  fireEvent.blur(within(style).getByLabelText("Line height"))
  const expected = { fontFamily: "serif", fontWeight: 700, fontSize: 36, lineHeight: 1.1, textAlign: "center", textWrap: "wrap", overflow: "clip" }
  expect(designNodes(state.doc!).find(node => node.id === "t")).toMatchObject(expected)
  // A later change of something else, as an older client makes it, keeps the style.
  fireEvent.change(screen.getByLabelText("x"), { target: { value: "12" } })
  fireEvent.blur(screen.getByLabelText("x"))
  fireEvent.change(screen.getByLabelText("Text content"), { target: { value: "New headline" } })
  expect(designNodes(state.doc!).find(node => node.id === "t")).toMatchObject({ ...expected, x: 12, text: "New headline" })
 })

 it("offers no text style controls while the build flag is off, but still draws stored style", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_TEXT_STYLE", "false")
  upsertDesignNode(state.doc!, { ...text({ fontSize: 30, fontFamily: "mono" }), name: "Headline" })
  const { container } = render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Headline" }))
  expect(screen.getByLabelText("Text content")).toBeInTheDocument()
  expect(screen.queryByRole("group", { name: "Text style" })).toBeNull()
  expect(container.querySelector('text[font-size="30"]')).toHaveAttribute("font-family", "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace")
 })

 it("shows text style read-only to a reader", () => {
  state.editable = false
  upsertDesignNode(state.doc!, { ...text({ fontSize: 30 }), name: "Headline" })
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Headline" }))
  const style = screen.getByRole("group", { name: "Text style" })
  expect(within(style).getByLabelText("Font size")).toHaveValue(30)
  for (const control of within(style).getAllByRole("combobox")) expect(control).toBeDisabled()
  expect(within(style).getByLabelText("Wrap text to the box width")).toBeDisabled()
 })
})
