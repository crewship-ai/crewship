import { afterEach, describe, expect, it, vi } from "vitest"
import { EDITOR_DISABLED_ERROR, EDITOR_SESSION_EXPIRED_ERROR, SESSION_EXPIRED_ERROR, WORKBOOK_COMMITTED_EVENT, WORKBOOK_EDITOR_FLAG, WORKBOOK_SESSION_OPEN_RETRIES, WORKBOOK_SESSION_REOPEN_MAX_MS, createEditorFetch, createWorkbookEditorClient, editorFrameSrc, editorRefusal, flagEnabled, parseCommittedPush } from "./editor-client"

const DIGEST = "a".repeat(64)
const HOST = "https://crewship.test"
const session = { session_id: "ws_s1", actor_label: "Owner", rev: 3, generation: 1, protocol: 1, bundle_digest: DIGEST, editor_url: `https://editor.test/b/${DIGEST}/index.html` }

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
    const src = editorFrameSrc(`https://editor.test/b/${DIGEST}/index.html?token=secret&host=x#frag`, DIGEST, HOST)
    expect(src).toBe(`https://editor.test/b/${DIGEST}/index.html?host=authority&parent_origin=${encodeURIComponent(HOST)}&v=1`)
  })

  afterEach(() => { vi.unstubAllEnvs() })

  it("allows plain HTTP on loopback only, and only outside production builds", () => {
    expect(editorFrameSrc(`http://localhost:9000/b/${DIGEST}/index.html`, DIGEST, "http://127.0.0.1:3000")).toContain("http://localhost:9000/")
    expect(editorFrameSrc(`http://editor.test/b/${DIGEST}/index.html`, DIGEST, HOST)).toBeNull()
    expect(editorFrameSrc(`http://localhost:9000/b/${DIGEST}/index.html`, DIGEST, "http://127.0.0.1:3000", { allowLoopbackHttp: false })).toBeNull()
    vi.stubEnv("NODE_ENV", "production")
    for (const host of ["localhost:9000", "127.0.0.1:9000", "[::1]:9000"]) expect(editorFrameSrc(`http://${host}/b/${DIGEST}/index.html`, DIGEST, "http://127.0.0.1:3000")).toBeNull()
    expect(editorFrameSrc(`https://editor.test/b/${DIGEST}/index.html`, DIGEST, HOST)).not.toBeNull()
    // An explicit test hook may still allow it.
    expect(editorFrameSrc(`http://localhost:9000/b/${DIGEST}/index.html`, DIGEST, "http://127.0.0.1:3000", { allowLoopbackHttp: true })).toContain("http://localhost:9000/")
  })

  it.each([
    ["no URL", undefined],
    ["not a URL", "editor"],
    ["the host's own origin outside the same-host editor path", `${HOST}/b/${DIGEST}/index.html`],
    ["another file under the same-host editor path", `${HOST}/_editor/b/${DIGEST}/app.js`],
    ["an encoded traversal under the same-host editor path", `${HOST}/_editor/b/..%2f${DIGEST}/index.html`],
    ["an encoded prefix", `${HOST}/%5Feditor/b/${DIGEST}/index.html`],
    ["an upper-case prefix", `${HOST}/_EDITOR/b/${DIGEST}/index.html`],
    ["a doubled slash", `${HOST}//_editor/b/${DIGEST}/index.html`],
    ["a short digest under the same-host editor path", `${HOST}/_editor/b/${DIGEST.slice(1)}/index.html`],
    ["a long digest under the same-host editor path", `${HOST}/_editor/b/${DIGEST}a/index.html`],
    ["another bundle under the same-host editor path", `${HOST}/_editor/b/${"e".repeat(64)}/index.html`],
    ["a traversal out of the same-host editor path", `${HOST}/_editor/b/${DIGEST}/../../api/v1/index.html`],
    ["the same-host path on a dedicated editor origin", `https://editor.test/_editor/b/${DIGEST}/index.html`],
    ["credentials in the URL", `https://u:p@editor.test/b/${DIGEST}/index.html`],
    ["an unpinned path", "https://editor.test/index.html"],
    ["a short digest", "https://editor.test/b/abc/index.html"],
    ["an upper-case digest", `https://editor.test/b/${DIGEST.toUpperCase()}/index.html`],
    ["another file", `https://editor.test/b/${DIGEST}/app.js`],
    ["a javascript: URL", "javascript:alert(1)"],
    ["another bundle than the session's pin", `https://editor.test/b/${"e".repeat(64)}/index.html`],
  ])("refuses %s", (_, url) => {
    expect(editorFrameSrc(url, DIGEST, HOST)).toBeNull()
  })

  it("frames the zero-config editor on the host's own origin only under its same-host path", () => {
    expect(editorFrameSrc(`${HOST}/_editor/b/${DIGEST}/index.html?token=x`, DIGEST, HOST)).toBe(`${HOST}/_editor/b/${DIGEST}/index.html?host=authority&parent_origin=${encodeURIComponent(HOST)}&v=1`)
    // Plain HTTP for the same host only on loopback outside production builds, like a dedicated origin.
    expect(editorFrameSrc(`http://127.0.0.1:3000/_editor/b/${DIGEST}/index.html`, DIGEST, "http://127.0.0.1:3000")).toContain("/_editor/b/")
    expect(editorFrameSrc(`http://studio.test/_editor/b/${DIGEST}/index.html`, DIGEST, "http://studio.test")).toBeNull()
    expect(editorFrameSrc(`${HOST}/_editor/b/${DIGEST}/index.html`, "", HOST)).toBeNull()
  })

  it("resolves the server's relative same-host editor URL against the host origin", () => {
    expect(editorFrameSrc(`/_editor/b/${DIGEST}/index.html`, DIGEST, HOST)).toBe(`${HOST}/_editor/b/${DIGEST}/index.html?host=authority&parent_origin=${encodeURIComponent(HOST)}&v=1`)
    expect(editorFrameSrc(`/_editor/b/${DIGEST}/index.html`, DIGEST, "http://127.0.0.1:3000", { allowLoopbackHttp: true })).toContain("http://127.0.0.1:3000/_editor/b/")
    expect(editorFrameSrc(`/_editor/b/${DIGEST}/index.html`, DIGEST, "http://studio.test", { allowLoopbackHttp: false })).toBeNull()
  })

  it.each([
    ["a relative path outside the editor prefix", `/b/${DIGEST}/index.html`],
    ["a relative path to another file", `/_editor/b/${DIGEST}/app.js`],
    ["a relative path with another digest", `/_editor/b/${"e".repeat(64)}/index.html`],
    ["a relative traversal", `/_editor/b/${DIGEST}/../../api/v1/index.html`],
    ["a protocol-relative URL", `//editor.test/b/${DIGEST}/index.html`],
    ["a path without a leading slash", `_editor/b/${DIGEST}/index.html`],
  ])("refuses %s", (_, url) => {
    expect(editorFrameSrc(url, DIGEST, HOST)).toBeNull()
  })

  it.each([
    ["an empty pin (no editor origin configured)", ""],
    ["a prefixed pin", `sha256:${DIGEST}`],
    ["an upper-case pin", DIGEST.toUpperCase()],
  ])("refuses every URL for %s", (_, pin) => {
    expect(editorFrameSrc(`https://editor.test/b/${DIGEST}/index.html`, pin, HOST)).toBeNull()
  })
})

describe("editorRefusal", () => {
  it.each([
    [503, { error: EDITOR_DISABLED_ERROR }, "disabled"],
    // Any other 503 (the workbook runtime itself) is transient.
    [503, { error: "Workbook unavailable" }, null],
    [503, null, null],
    [401, { error: SESSION_EXPIRED_ERROR }, "expired"],
    // The editor's own code, whatever status the server settles on: reopen, never "access lost".
    [409, { error: EDITOR_SESSION_EXPIRED_ERROR }, "expired"],
    [401, { error: EDITOR_SESSION_EXPIRED_ERROR }, "expired"],
    [404, { error: EDITOR_SESSION_EXPIRED_ERROR }, "expired"],
    [401, { error: "unauthorized" }, "access"],
    [401, null, "access"],
    [403, { error: "Workbook not found" }, "access"],
    // An edit whose recalculation cannot be confirmed is refused; access is intact.
    [403, { error: "effect_out_of_mandate" }, null],
    [404, { error: "Workbook not found" }, "access"],
    [409, { error: "in_progress" }, null],
    [500, null, null],
    [200, {}, null],
  ])("%i %j → %s", (status, body, expected) => {
    expect(editorRefusal({ status, body })).toBe(expected)
  })
})

describe("createEditorFetch: editor requests never reach the app-wide sign-in expiry handling", () => {
  const respond = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
  const setup = (answers: Response[], refresh: "ok" | "auth_failed" | "retryable_failed" = "ok", bearer = false) => {
    const apiFetch = vi.fn(async (_url: string, _init?: RequestInit & { skipRefresh?: boolean }) => answers.shift()!)
    const tryRefresh = vi.fn(async () => refresh)
    return { apiFetch, tryRefresh, editorFetch: createEditorFetch({ apiFetch, tryRefresh, bearer: () => bearer }) }
  }

  it("asks apiFetch to skip its own refresh and sign-out handling on every request", async () => {
    const t = setup([respond(200, {})])
    await t.editorFetch("/workbook/x", { method: "POST", body: "{}" })
    expect(t.apiFetch).toHaveBeenCalledWith("/workbook/x", { method: "POST", body: "{}", skipRefresh: true })
  })

  it("hands an editor-session expiry straight to the bridge: no refresh, no replay", async () => {
    const t = setup([respond(401, { error: EDITOR_SESSION_EXPIRED_ERROR })])
    const response = await t.editorFetch("/workbook/x")
    expect(response.status).toBe(401)
    expect(t.tryRefresh).not.toHaveBeenCalled()
    expect(t.apiFetch).toHaveBeenCalledTimes(1)
  })

  it("refreshes an expired access token itself and replays the request once", async () => {
    const t = setup([respond(401, { error: SESSION_EXPIRED_ERROR }), respond(200, { ok: true })])
    const response = await t.editorFetch("/workbook/x", { method: "POST", body: "{}" })
    expect(response.status).toBe(200)
    expect(t.tryRefresh).toHaveBeenCalledTimes(1)
    expect(t.apiFetch).toHaveBeenCalledTimes(2)
    expect(t.apiFetch.mock.calls[1][1]).toEqual({ method: "POST", body: "{}", skipRefresh: true })
  })

  it("reports a refresh that could not be done as a transient 503, not as lost access", async () => {
    const t = setup([respond(401, { error: SESSION_EXPIRED_ERROR })], "retryable_failed")
    expect((await t.editorFetch("/workbook/x")).status).toBe(503)
  })

  it.each([
    ["the sign-in is gone", "session_expired", "auth_failed", false, 1],
    ["a revoked sign-in", "session_revoked", "ok", false, 0],
    ["bearer mode (desktop shell)", "session_expired", "ok", true, 0],
  ] as const)("returns the 401 as it is for %s, without the app-wide handling", async (_, error, refresh, bearer, refreshes) => {
    const t = setup([respond(401, { error })], refresh, bearer)
    expect((await t.editorFetch("/workbook/x")).status).toBe(401)
    expect(t.apiFetch).toHaveBeenCalledTimes(1)
    expect(t.tryRefresh).toHaveBeenCalledTimes(refreshes)
  })
})

describe("workbook.committed push", () => {
  it("reads the revision of this Page only", () => {
    expect(WORKBOOK_COMMITTED_EVENT).toBe("workbook.committed")
    expect(parseCommittedPush({ page_id: "p1", rev: 7, generation: 1, kind: "cells", cells: 2 }, "p1")).toEqual({ rev: 7, generation: 1 })
    expect(parseCommittedPush({ page_id: "p2", rev: 7, generation: 1 }, "p1")).toBeNull()
    expect(parseCommittedPush({ page_id: "p1", rev: "7", generation: 1 }, "p1")).toBeNull()
    expect(parseCommittedPush(null, "p1")).toBeNull()
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

  it("reports a failed flag read instead of treating it as off", async () => {
    await expect(createWorkbookEditorClient({ workspaceId: "w", pageId: "p", fetch: async () => respond(500, {}) }).editorEnabled()).rejects.toThrow()
    await expect(createWorkbookEditorClient({ workspaceId: "w", pageId: "p", fetch: async () => { throw new Error("offline") } }).editorEnabled()).rejects.toThrow()
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
    // No editor origin configured yet: the server sends an empty pin and no URL. Fail closed.
    [201, { ...session, bundle_digest: "", editor_url: undefined }, 0],
    [201, { ...session, bundle_digest: `sha256:${DIGEST}` }, 0],
    [201, { ...session, rev: 0 }, 0],
  ])("refuses a session answer %i %j", async (status, body, expected) => {
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p", fetch: async () => respond(status, body) })
    await expect(client.openSession()).resolves.toMatchObject({ ok: false, status: expected })
  })

  it("closes a session the server opened but the host cannot use (no bundle pin yet), so it does not count against the cap", async () => {
    const fetch = vi.fn(async (_url: string, init?: RequestInit) => init?.method === "DELETE" ? respond(200, { status: "closed" }) : respond(201, { ...session, bundle_digest: "", editor_url: undefined }))
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch })
    await expect(client.openSession()).resolves.toEqual({ ok: false, status: 0 })
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/pages/%40p1/workbook/sessions/ws_s1?workspace_id=w", { method: "DELETE", cache: "no-store" })
  })

  it("retries a 429 on open after Retry-After, a bounded number of times", async () => {
    const sleeps: number[] = []
    const busy = () => new Response(JSON.stringify({ error: "too many open editor sessions; close one first" }), { status: 429, headers: { "content-type": "application/json", "retry-after": "2" } })
    const answers = [busy(), respond(201, session)]
    const fetch = vi.fn(async () => answers.shift()!)
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch, sleep: async ms => { sleeps.push(ms) } })
    await expect(client.openSession()).resolves.toEqual({ ok: true, session })
    expect(sleeps).toEqual([2000])

    const always = vi.fn(async () => new Response(JSON.stringify({ error: "busy" }), { status: 429 }))
    const waits: number[] = []
    const stuck = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch: always, sleep: async ms => { waits.push(ms) } })
    await expect(stuck.openSession()).resolves.toMatchObject({ ok: false, status: 429 })
    expect(always).toHaveBeenCalledTimes(WORKBOOK_SESSION_OPEN_RETRIES + 1)
    // No Retry-After: a short default, and never longer than the cap.
    expect(waits.every(ms => ms > 0 && ms <= 10_000)).toBe(true)
  })

  it("returns a 429 with a long Retry-After at once, with the wait, instead of retrying it", async () => {
    const full = (seconds: string) => vi.fn(async () => new Response(JSON.stringify({ error: "too_many_editor_sessions" }), { status: 429, headers: { "content-type": "application/json", "retry-after": seconds } }))
    const sleeps: number[] = []
    const fetch = full("900")
    const client = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch, sleep: async ms => { sleeps.push(ms) } })
    await expect(client.openSession()).resolves.toEqual({ ok: false, status: 429, retryAfterMs: 900_000 })
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(sleeps).toEqual([])
    // A wait beyond the reopen cap is reported at the cap.
    const later = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch: full("86400"), sleep: async () => {} })
    await expect(later.openSession()).resolves.toEqual({ ok: false, status: 429, retryAfterMs: WORKBOOK_SESSION_REOPEN_MAX_MS })
    // Short waits are retried in the call; when they run out the last wait is reported.
    const short = createWorkbookEditorClient({ workspaceId: "w", pageId: "p1", fetch: full("2"), sleep: async () => {} })
    await expect(short.openSession()).resolves.toEqual({ ok: false, status: 429, retryAfterMs: 2000 })
  })

  it("names the refusal of a session it could not open", async () => {
    const open = (status: number, body: unknown) => createWorkbookEditorClient({ workspaceId: "w", pageId: "p", fetch: async () => respond(status, body) }).openSession()
    await expect(open(503, { error: EDITOR_DISABLED_ERROR })).resolves.toEqual({ ok: false, status: 503, refusal: "disabled" })
    await expect(open(404, { error: "Workbook not found" })).resolves.toEqual({ ok: false, status: 404, refusal: "access" })
    await expect(open(500, null)).resolves.toEqual({ ok: false, status: 500 })
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
      ["another bundle", { ...session, bundle_digest: "e".repeat(64) }],
      ["the same pin in another format", { ...session, bundle_digest: `sha256:${DIGEST}` }],
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
