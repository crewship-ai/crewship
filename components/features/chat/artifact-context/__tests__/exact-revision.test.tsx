import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { acceptCheckpoint, checkpointPath, readExactRevision } from "../exact-revision"
import { PinnedRevisionBody } from "../pinned-revision-body"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
const pin = { workspaceId: "w", pageId: "p1", mode: "revision" as const, revision: 3, checkpointId: "cp_9" }
const content = { type: "doc", content: [{ type: "paragraph", attrs: { id: "p" }, content: [{ type: "text", text: "x" }] }] }
const good = { page_id: "p1", id: "cp_9", rev: 3, schema: 3, content }

it("reads through the checkpoint endpoint and never with a revision query", async () => {
  const fetcher = vi.fn(async (_path: string, _signal?: AbortSignal) => ({ status: 200, body: good as unknown }))
  const read = await readExactRevision(fetcher, pin)
  // Workspace-scoped; the revision never travels as a query.
  expect(fetcher.mock.calls[0][0]).toBe("/api/v1/pages/%40p1/document/checkpoints/cp_9?workspace_id=w")
  expect(checkpointPath(pin)).not.toMatch(/revision=/)
  expect(read.status).toBe("ready")
})
it.each([
  ["another page", { ...good, page_id: "p2" }],
  ["another checkpoint", { ...good, id: "cp-other" }],
  ["latest content (different rev)", { ...good, rev: 7 }],
  ["no content", { ...good, content: undefined }],
  ["bad schema", { ...good, schema: "x" }],
  ["schema -1", { ...good, schema: -1 }],
  ["schema 1 (intentionally not rendered by this reader)", { ...good, schema: 1 }],
  ["unknown schema 99", { ...good, schema: 99 }],
  ["arbitrary non-empty content", { ...good, content: { anything: ["goes"] } }],
  ["a document node that does not validate", { ...good, content: { type: "doc" } }],
])("a response with %s is never shown as the pinned revision", (_name, body) => {
  expect(acceptCheckpoint(pin, body)).toEqual({ status: "unavailable" })
})
it("does not even request a checkpoint id outside the backend's bounded shape", async () => {
  const fetcher = vi.fn(async () => ({ status: 200, body: good as unknown }))
  for (const checkpointId of ["cp 9", "a/b", "", "x".repeat(129)]) {
    expect(await readExactRevision(fetcher, { ...pin, checkpointId })).toEqual({ status: "unavailable" })
  }
  expect(fetcher).not.toHaveBeenCalled()
})
it("keeps missing, denied and temporary failures distinct", async () => {
  const status = (code: number) => readExactRevision(async () => ({ status: code, body: null }), pin)
  expect((await status(404)).status).toBe("unavailable")
  // A malformed or conflicting request is permanent: no retry loop.
  expect((await status(400)).status).toBe("unavailable")
  expect((await status(409)).status).toBe("unavailable")
  expect((await status(403)).status).toBe("denied")
  expect((await status(503)).status).toBe("error")
  expect((await readExactRevision(async () => { throw new Error("offline") }, pin)).status).toBe("error")
})
it("shows 'not available' instead of any content when the pin cannot be read", () => {
  const body = (read: Parameters<typeof PinnedRevisionBody>[0]["read"]) => render(<PinnedRevisionBody read={read}>{() => <p>LATEST CONTENT</p>}</PinnedRevisionBody>)
  body({ status: "unavailable" })
  expect(screen.getByRole("alert").textContent).toBe("This revision is not available.")
  expect(screen.queryByText("LATEST CONTENT")).toBeNull()
  cleanup()
  body({ status: "error" })
  expect(screen.getByRole("alert").textContent).toMatch(/not removed/)
  cleanup()
  body({ status: "ready", target: pin, schema: 3, content: {} })
  expect(screen.getByText("LATEST CONTENT")).toBeTruthy()
})
it("renders nothing with the flag off", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "")
  expect(render(<PinnedRevisionBody read={{ status: "unavailable" }}>{() => null}</PinnedRevisionBody>).container.innerHTML).toBe("")
})
