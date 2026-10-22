import { describe, expect, it, vi } from "vitest"
import { WORKBOOK_EDITOR_FLAG, createWorkbookEditorClient, editorFrameSrc, flagEnabled } from "./editor-client"

const DIGEST = "a".repeat(64)
const HOST = "https://crewship.test"
const session = { session_id: "ws_s1", actor_label: "Owner", rev: 3, generation: 1, protocol: 1, bundle_digest: `sha256:${DIGEST}`, editor_url: `https://editor.test/b/${DIGEST}/index.html` }

describe("flagEnabled", () => {
  it.each([
    [[{ key: WORKBOOK_EDITOR_FLAG, enabled: true }], true],
    [[{ key: WORKBOOK_EDITOR_FLAG, enabled: false }], false],
    [[{ key: WORKBOOK_EDITOR_FLAG, enabled: false, override_enabled: true }], true],
    [[{ key: WORKBOOK_EDITOR_FLAG, enabled: true, override_enabled: false }], false],
    [[{ key: "other", enabled: true }], false],
    [[{ key: WORKBOOK_EDITOR_FLAG, enabled: "true" }], false],
    [[], false],
    [{ key: WORKBOOK_EDITOR_FLAG, enabled: true }, false],
    [null, false],
  ])("%j → %s (the workspace override wins; anything else is off)", (body, expected) => {
    expect(flagEnabled(body, WORKBOOK_EDITOR_FLAG)).toBe(expected)
  })
})

describe("editorFrameSrc", () => {
  it("replaces the query with the bridge mode and the host origin only", () => {
    const src = editorFrameSrc(`https://editor.test/b/${DIGEST}/index.html?token=secret&host=x#frag`, HOST)
    expect(src).toBe(`https://editor.test/b/${DIGEST}/index.html?host=authority&parent_origin=${encodeURIComponent(HOST)}&v=1`)
  })

  it("allows plain HTTP on loopback only", () => {
    expect(editorFrameSrc(`http://localhost:9000/b/${DIGEST}/index.html`, "http://127.0.0.1:3000")).toContain("http://localhost:9000/")
    expect(editorFrameSrc(`http://editor.test/b/${DIGEST}/index.html`, HOST)).toBeNull()
  })

  it.each([
    ["no URL", undefined],
    ["not a URL", "editor"],
    ["the host's own origin", `${HOST}/b/${DIGEST}/index.html`],
    ["credentials in the URL", `https://u:p@editor.test/b/${DIGEST}/index.html`],
    ["an unpinned path", "https://editor.test/index.html"],
    ["a short digest", "https://editor.test/b/abc/index.html"],
    ["an upper-case digest", `https://editor.test/b/${DIGEST.toUpperCase()}/index.html`],
    ["another file", `https://editor.test/b/${DIGEST}/app.js`],
    ["a javascript: URL", "javascript:alert(1)"],
  ])("refuses %s", (_, url) => {
    expect(editorFrameSrc(url, HOST)).toBeNull()
  })
})

describe("createWorkbookEditorClient", () => {
  const respond = (status: number, body: unknown) => new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "content-type": "application/json" } })

  it("reads the flag of this workspace without caching", async () => {
    const fetch = vi.fn(async () => respond(200, [{ key: WORKBOOK_EDITOR_FLAG, enabled: true }]))
    const client = createWorkbookEditorClient({ workspaceId: "ws 1", pageId: "p1", fetch })
    await expect(client.editorEnabled()).resolves.toBe(true)
    expect(fetch).toHaveBeenCalledWith("/api/v1/feature-flags?workspace_id=ws+1", { cache: "no-store" })
  })

  it("treats a failed flag read as off", async () => {
    await expect(createWorkbookEditorClient({ workspaceId: "w", pageId: "p", fetch: async () => respond(500, {}) }).editorEnabled()).resolves.toBe(false)
    await expect(createWorkbookEditorClient({ workspaceId: "w", pageId: "p", fetch: async () => { throw new Error("offline") } }).editorEnabled()).resolves.toBe(false)
  })

  it("opens a session on the Page's workbook route", async () => {
    const fetch = vi.fn(async () => respond(201, session))
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch })
    await expect(client.openSession()).resolves.toEqual({ ok: true, session })
    expect(fetch).toHaveBeenCalledWith("/api/v1/pages/%40p1/workbook/sessions?workspace_id=w", expect.objectContaining({ method: "POST", cache: "no-store", body: JSON.stringify({ protocol: 1 }) }))
  })

  it.each([
    [503, {}, 503],
    [404, { error: "Workbook not found" }, 404],
    [200, { session_id: "x" }, 0],
    [200, { ...session, protocol: 2 }, 0],
  ])("refuses a session answer %i %j", async (status, body, expected) => {
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p", fetch: async () => respond(status, body) })
    await expect(client.openSession()).resolves.toEqual({ ok: false, status: expected })
  })

  it("reads the head from one commits entry", async () => {
    const fetch = vi.fn(async () => respond(200, { head_rev: 9, generation: 1, commits: [] }))
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch })
    await expect(client.head(7)).resolves.toEqual({ ok: true, head: { rev: 9, generation: 1 } })
    expect(fetch).toHaveBeenCalledWith("/api/v1/pages/%40p1/workbook/commits?workspace_id=w&since=7&limit=1", { cache: "no-store" })
    const refused = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch: async () => respond(410, { error: "snapshot_required" }) })
    await expect(refused.head(1)).resolves.toEqual({ ok: false, status: 410 })
  })

  it("closes the session it opened through the bridge transport", async () => {
    const fetch = vi.fn(async () => respond(204, undefined))
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch })
    await client.transport(session).closeSession()
    expect(fetch).toHaveBeenCalledWith("/api/v1/pages/%40p1/workbook/sessions/ws_s1?workspace_id=w", { method: "DELETE", cache: "no-store" })
  })
})
