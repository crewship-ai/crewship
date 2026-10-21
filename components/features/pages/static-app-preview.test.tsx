import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup } from "@testing-library/react"
import { StaticAppPreview } from "./static-app-preview"

const prepare = vi.hoisted(() => vi.fn(async (html: string) => html))
vi.mock("@/lib/pages/artifact-static", () => ({ staticArtifactDocument: prepare }))
beforeEach(() => vi.stubEnv("NEXT_PUBLIC_STATIC_APP_PREVIEW", "true"))
afterEach(() => { prepare.mockClear(); cleanup(); vi.unstubAllEnvs() })
const publication = { workspaceId: "workspace", pageId: "page-a", revision: 1, profile: "static-html-v1", html: "<h1>Version one</h1>" }

it("T-AX-05: grants no iframe execution, origin, navigation or referrer capability", async () => {
  render(<StaticAppPreview publication={publication} />)
  const frame = await screen.findByTitle("App static preview")
  await waitFor(() => expect(frame.getAttribute("srcdoc")).toContain("Version one"))
  expect(frame.getAttribute("sandbox")).toBe("")
  expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer")
})

it("T-AX-04: pins publication until the reader explicitly loads the next version", async () => {
  const { rerender } = render(<StaticAppPreview publication={publication} />)
  await waitFor(() => expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).toContain("Version one"))
  rerender(<StaticAppPreview publication={{ ...publication, revision: 2, html: "<h1>Version two</h1>" }} />)
  expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).toContain("Version one")
  fireEvent.click(screen.getByRole("button", { name: "Load version 2" }))
  await waitFor(() => expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).toContain("Version two"))
})

it("T-AX-05: access loss, identity changes and unsupported profiles remove the old frame immediately", () => {
  const { rerender } = render(<StaticAppPreview publication={publication} />)
  rerender(<StaticAppPreview publication={null} />)
  expect(screen.queryByTitle("App static preview")).toBeNull()
  rerender(<StaticAppPreview publication={publication} />)
  expect(screen.queryByTitle("App static preview")).toBeNull()
  rerender(<StaticAppPreview publication={{ ...publication, pageId: "replacement" }} />)
  expect(screen.queryByTitle("App static preview")).toBeNull()
  rerender(<StaticAppPreview publication={{ ...publication, profile: "interactive-v1" }} />)
  expect(screen.queryByTitle("App static preview")).toBeNull()
})

it("renders nothing while the flag is off", () => {
  vi.stubEnv("NEXT_PUBLIC_STATIC_APP_PREVIEW", "")
  const { container } = render(<StaticAppPreview publication={publication} />)
  expect(container.innerHTML).toBe("")
  expect(prepare).not.toHaveBeenCalled()
})

it("allows the frame no browser features", async () => {
  render(<StaticAppPreview publication={publication} />)
  expect((await screen.findByTitle("App static preview")).getAttribute("allow")).toBe("")
})

it("a revoke and regrant batched into one render needs the caller's key to drop the old HTML", async () => {
  const { rerender } = render(<StaticAppPreview key="a" publication={publication} />)
  await waitFor(() => expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).toContain("Version one"))
  rerender(<StaticAppPreview key="b" publication={{ ...publication, revision: 2, html: "<h1>Regranted</h1>" }} />)
  expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).not.toContain("Version one")
  await waitFor(() => expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).toContain("Regranted"))
})

it("shows a failure and no frame content when preparation rejects", async () => {
  prepare.mockRejectedValueOnce(new Error("boom"))
  render(<StaticAppPreview publication={publication} />)
  expect((await screen.findByRole("alert")).textContent).toContain("could not be prepared")
  expect(screen.queryByTitle("App static preview")).toBeNull()
})

it("shows no frame when the preparation fails", async () => {
  render(<StaticAppPreview publication={{ ...publication, html: "<h1>Version one</h1>", revision: 0 }} />)
  expect(screen.queryByTitle("App static preview")).toBeNull()
  expect(screen.getByRole("alert")).toBeTruthy()
})

it("discards a preparation that resolves after revocation, even if access is regranted under a new key", async () => {
  let release: (document: string) => void = () => {}
  prepare.mockImplementationOnce(() => new Promise<string>(resolve => { release = resolve }))
  const { rerender } = render(<StaticAppPreview key="old" publication={publication} />)
  rerender(<StaticAppPreview key="old" publication={null} />)
  release("<h1>Old identity</h1>")
  await Promise.resolve()
  expect(screen.queryByTitle("App static preview")).toBeNull()
  rerender(<StaticAppPreview key="new" publication={{ ...publication, revision: 2, html: "<h1>Regranted</h1>" }} />)
  await waitFor(() => expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).toContain("Regranted"))
  expect(screen.getByTitle("App static preview").getAttribute("srcdoc")).not.toContain("Old identity")
})
