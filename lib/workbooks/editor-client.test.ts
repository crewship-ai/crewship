import { afterEach, describe, expect, it, vi } from "vitest"
import { EDITOR_DISABLED_ERROR, SESSION_EXPIRED_ERROR, WORKBOOK_EDITOR_FLAG, createWorkbookEditorClient, editorFrameSrc, editorRefusal, flagEnabled } from "./editor-client"

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

  afterEach(() => { vi.unstubAllEnvs() })

  it("allows plain HTTP on loopback only, and only outside production builds", () => {
    expect(editorFrameSrc(`http://localhost:9000/b/${DIGEST}/index.html`, "http://127.0.0.1:3000")).toContain("http://localhost:9000/")
    expect(editorFrameSrc(`http://editor.test/b/${DIGEST}/index.html`, HOST)).toBeNull()
    expect(editorFrameSrc(`http://localhost:9000/b/${DIGEST}/index.html`, "http://127.0.0.1:3000", { allowLoopbackHttp: false })).toBeNull()
    vi.stubEnv("NODE_ENV", "production")
    for (const host of ["localhost:9000", "127.0.0.1:9000", "[::1]:9000"]) expect(editorFrameSrc(`http://${host}/b/${DIGEST}/index.html`, "http://127.0.0.1:3000")).toBeNull()
    expect(editorFrameSrc(`https://editor.test/b/${DIGEST}/index.html`, HOST)).not.toBeNull()
    // An explicit test hook may still allow it.
    expect(editorFrameSrc(`http://localhost:9000/b/${DIGEST}/index.html`, "http://127.0.0.1:3000", { allowLoopbackHttp: true })).toContain("http://localhost:9000/")
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

describe("editorRefusal", () => {
  it.each([
    [503, { error: EDITOR_DISABLED_ERROR }, "disabled"],
    // Any other 503 (the workbook runtime itself) is transient.
    [503, { error: "Workbook unavailable" }, null],
    [503, null, null],
    [401, { error: SESSION_EXPIRED_ERROR }, "expired"],
    [401, { error: "unauthorized" }, "access"],
    [401, null, "access"],
    [403, { error: "effect_out_of_mandate" }, "access"],
    [404, { error: "Workbook not found" }, "access"],
    [409, { error: "in_progress" }, null],
    [500, null, null],
    [200, {}, null],
  ])("%i %j → %s", (status, body, expected) => {
    expect(editorRefusal({ status, body })).toBe(expected)
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

  it("names the refusal of a head read: editor turned off, session over, access lost", async () => {
    const head = (status: number, body: unknown) => createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch: async () => respond(status, body) }).head(1)
    await expect(head(503, { error: EDITOR_DISABLED_ERROR })).resolves.toEqual({ ok: false, status: 503, refusal: "disabled" })
    await expect(head(404, { error: "Workbook not found" })).resolves.toEqual({ ok: false, status: 404, refusal: "access" })
    await expect(head(503, { error: "Workbook unavailable" })).resolves.toEqual({ ok: false, status: 503 })
  })

  describe("checkSession", () => {
    const check = (respondWith: () => Promise<Response>) => {
      const fetch = vi.fn(respondWith)
      return { fetch, result: createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch }).checkSession(session) }
    }

    it("renews the live session and finds the same bundle", async () => {
      const { fetch, result } = check(async () => respond(200, session))
      await expect(result).resolves.toBe("live")
      expect(fetch).toHaveBeenCalledWith("/api/v1/pages/%40p1/workbook/sessions?workspace_id=w", expect.objectContaining({ method: "POST", cache: "no-store", body: JSON.stringify({ session_id: "ws_s1", protocol: 1 }) }))
    })

    it.each([
      ["another bundle", { ...session, bundle_digest: "sha256:other" }],
      ["no bundle at all", { ...session, bundle_digest: "" }],
    ])("calls the bundle revoked when the server now names %s", async (_, body) => {
      await expect(check(async () => respond(200, body)).result).resolves.toBe("revoked")
    })

    it.each([
      [503, { error: EDITOR_DISABLED_ERROR }, "disabled"],
      [401, { error: SESSION_EXPIRED_ERROR }, "expired"],
      [404, { error: "Workbook not found" }, "access"],
      [503, { error: "Workbook unavailable" }, "unknown"],
      [500, null, "unknown"],
      [200, null, "unknown"],
    ])("%i %j → %s", async (status, body, expected) => {
      await expect(check(async () => respond(status, body)).result).resolves.toBe(expected)
    })

    it("knows nothing on a network failure", async () => {
      await expect(check(async () => { throw new TypeError("offline") }).result).resolves.toBe("unknown")
    })
  })

  it("closes the session it opened through the bridge transport", async () => {
    const fetch = vi.fn(async () => respond(204, undefined))
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch })
    await client.transport(session).closeSession()
    expect(fetch).toHaveBeenCalledWith("/api/v1/pages/%40p1/workbook/sessions/ws_s1?workspace_id=w", { method: "DELETE", cache: "no-store" })
  })
})
