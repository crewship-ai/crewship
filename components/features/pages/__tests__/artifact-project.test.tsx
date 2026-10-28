import React from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
const state = vi.hoisted(() => ({ user: "owner", token: "", comment: vi.fn(), data: { can_write: true, latest_version: 2, shared_version: 1, manifest: { version: 2, entrypoint: "index.html", files: [{ path: "index.html", content: "<h1>Hello</h1>", media_type: "text/html" }, { path: "details.html", content: "<h1>Details</h1>", media_type: "text/html" }] }, versions: [{ version: 2 }, { version: 1 }], quota: { used_bytes: 10, limit_bytes: 100, warning: false }, comments: [] }, restore: vi.fn(), share: vi.fn() }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ dispatch: vi.fn(), ready: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/hooks/use-artifact-project", () => ({ useArtifactProject: () => ({ query: { data: state.data }, restore: { mutate: state.restore }, share: { mutate: state.share }, comment: { mutate: state.comment } }) }))
vi.mock("@/lib/pages/artifact-project-preview", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/pages/artifact-project-preview")>(), artifactProjectDocument: vi.fn((_files: unknown, _path: string, token: string) => { state.token = token; return "safe document" }) }))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-slides-preview", () => ({ ArtifactSlidesPreview: () => <div>Typed Page Slides</div> }))
import { artifactProjectDocument } from "@/lib/pages/artifact-project-preview"
import { ArtifactProject } from "../artifact-project"
afterEach(() => { cleanup(); vi.clearAllMocks(); state.data.can_write = true; state.user = "owner"; localStorage.clear() })
it("switches screens, restores with the latest base and shares only the selected version", () => {
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(screen.getByRole("button", { name: "details.html" }))
 expect(screen.getByTitle("Artifact preview")).toHaveAttribute("data-screen", "details.html")
 fireEvent.change(screen.getByLabelText("Version"), { target: { value: "1" } })
 fireEvent.click(screen.getByRole("button", { name: "Restore version 1" }))
 expect(state.restore).toHaveBeenCalledWith({ version: 1, base_version: 2 }, expect.objectContaining({ onSuccess: expect.any(Function) }))
 fireEvent.click(screen.getByRole("button", { name: "Share version 1" }))
 expect(state.share).toHaveBeenCalledWith({ version: 1, base_version: 2 })
})
it("keeps all write controls unavailable to a reader", () => {
 state.data.can_write = false
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 expect(screen.queryByRole("button", { name: /Restore version/ })).toBeNull()
 expect(screen.queryByRole("button", { name: /Share version/ })).toBeNull()
 expect(screen.queryByRole("button", { name: "Send to agent" })).toBeNull()
 expect(screen.getByText("Read only")).toBeTruthy()
})

it("never moves an unsent draft into another user browser scope", () => {
 const storage = new Map<string,string>()
 vi.mocked(localStorage.getItem).mockImplementation(key => storage.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key,value) => { storage.set(key,value) })
 vi.mocked(localStorage.removeItem).mockImplementation(key => { storage.delete(key) })
 const view=render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.change(screen.getByLabelText("Comment for agent"),{target:{value:"Owner private draft"}})
 expect(localStorage.getItem("artifact-comment:owner:w:demo")).toBe("Owner private draft")
 state.user="reader"
 view.rerender(<ArtifactProject workspaceId="w" slug="demo" />)
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("")
 expect(localStorage.getItem("artifact-comment:reader:w:demo")).toBeNull()
})

it("retains 500 Unicode codepoints in a normalized selected quote sent to the agent", () => {
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 fireEvent.click(screen.getByRole("button", { name: "Select an element" }))
 const frame = screen.getByTitle("Artifact preview") as HTMLIFrameElement
 const quote = "  \ufeffŽluťoučký\u0085  kůň\u00a0pěl\nďábelské\tódy  " + "🙂".repeat(520)
 fireEvent(window, new MessageEvent("message", { origin: "null", source: frame.contentWindow, data: { token: state.token, selector: "#artifact-unicode-quote", quote } }))
 fireEvent.change(screen.getByLabelText("Comment for agent"), { target: { value: "Uprav nadpis" } })
 fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
 const expected = Array.from("\ufeffŽluťoučký kůň pěl ďábelské ódy " + "🙂".repeat(520)).slice(0,500).join("")
 expect(state.comment.mock.calls[0][0].quote).toBe(expected)
})

it("keeps the Page Project preview when the Dashboard flag is off and dashboard.json is only an asset", () => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "false")
 state.data.manifest.files.push({ path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["sales"]}' })
 try {
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  expect(screen.getByTitle("Artifact preview")).toHaveAttribute("srcdoc", "safe document")
  expect(screen.queryByText("Dashboard preview is unavailable.")).toBeNull()
 } finally { state.data.manifest.files.pop(); vi.unstubAllEnvs() }
})

it.each([["true", "slides", "typed"], ["true", undefined, "unsupported"], ["false", "slides", "legacy"]] as const)("keeps the Pages Slides route safe with flag %s", (flag, kind, mode) => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", flag)
 const previous = state.data
 state.data = Object.assign({}, previous, { artifact_kind: kind, page_id: "page", manifest: { ...previous.manifest, files: [...previous.manifest.files, { path: "deck.json", media_type: "application/json", content: '{}' }] } })
 try {
  render(<ArtifactProject workspaceId="w" slug="demo" />)
  if (mode === "legacy") expect(screen.getByTitle("Artifact preview")).toHaveAttribute("srcdoc", "safe document")
  else {
   expect(screen.getByText(mode === "typed" ? "Typed Page Slides" : "This Slides format is unsupported.")).toBeInTheDocument()
   expect(screen.queryByTitle("Artifact preview")).toBeNull()
   expect(artifactProjectDocument).not.toHaveBeenCalled()
  }
 } finally { state.data = previous; vi.unstubAllEnvs() }
})
