// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  INTERACTIVE_MAX_IN_FLIGHT, INTERACTIVE_REQUEST, INTERACTIVE_RESPONSE, InteractiveArtifactBroker, InteractiveFrameSession,
  createInteractiveDataAccess, interactiveArtifactSource, validateInteractiveRuntime, type InteractiveDataAccess,
} from "../interactive-artifact"

const collection = { collection: "votes", documents: [{ id: "yes", version: 1, value: { count: 1 }, updated_at: "2026-10-31T10:00:00Z", source_label: "You" }] }
function fakeAccess(): InteractiveDataAccess & { calls: string[] } {
  const calls: string[] = []
  return {
    calls,
    get: vi.fn(async (name: string) => { calls.push(`get:${name}`); return collection }),
    list: vi.fn(async () => { calls.push("list"); return { collections: [{ name: "votes", documents: 1 }] } }),
    batch: vi.fn(async (name: string) => { calls.push(`batch:${name}`); return collection }),
    viewer: () => ({ name: "Ada Lovelace" }),
  }
}

const open: MessageChannel[] = []
const brokers: InteractiveArtifactBroker[] = []
afterEach(() => { brokers.splice(0).forEach(broker => broker.close()); open.splice(0).forEach(channel => { channel.port1.close(); channel.port2.close() }) })

function connect(canWrite: boolean, access = fakeAccess()) {
  const channel = new MessageChannel()
  open.push(channel)
  brokers.push(new InteractiveArtifactBroker(channel.port1, access, { collections: ["votes"], canWrite }))
  const replies = new Map<number, Record<string, unknown>>()
  channel.port2.onmessage = event => { if (event.data?.type === INTERACTIVE_RESPONSE) replies.set(event.data.id, event.data) }
  const send = (id: number, method: unknown, params: unknown = {}, extra: Record<string, unknown> = {}, transfer: Transferable[] = []) =>
    channel.port2.postMessage({ type: INTERACTIVE_REQUEST, id, method, params, ...extra }, transfer)
  const reply = async (id: number) => { await vi.waitFor(() => expect(replies.has(id)).toBe(true)); return replies.get(id)! }
  return { access, send, reply, replies, channel }
}

describe("InteractiveArtifactBroker", () => {
  it.each([
    ["an unknown method", "fetch"],
    ["a prototype key", "__proto__"],
    ["a delete the server API has no method for", "data.delete"],
    ["a non-string method", 7],
  ])("refuses %s without calling the server", async (_, method) => {
    const { access, send, reply } = connect(true)
    send(1, method)
    expect(await reply(1)).toMatchObject({ error: "Unsupported request.", code: "invalid" })
    expect(access.calls).toEqual([])
  })

  it("allows exactly data.get, data.list, data.batch and viewer", async () => {
    const { access, send, reply } = connect(true)
    send(1, "data.get", { collection: "votes" })
    send(2, "data.list")
    send(3, "data.batch", { collection: "votes", documents: [{ id: "yes", ifVersion: 1, value: { count: 2 } }] })
    send(4, "viewer")
    expect((await reply(1)).result).toEqual(collection)
    expect((await reply(2)).result).toEqual({ collections: [{ name: "votes", documents: 1 }] })
    expect((await reply(3)).result).toEqual(collection)
    expect((await reply(4)).result).toEqual({ name: "Ada Lovelace" })
    expect(access.calls.sort()).toEqual(["batch:votes", "get:votes", "list"])
  })

  it("refuses a reader's write but still serves the read", async () => {
    const { access, send, reply } = connect(false)
    send(1, "data.batch", { collection: "votes", documents: [{ id: "yes", ifVersion: 1, value: { count: 2 } }] })
    send(2, "data.get", { collection: "votes" })
    expect(await reply(1)).toMatchObject({ error: "You can read this artifact but not change its data.", code: "forbidden" })
    expect((await reply(2)).result).toEqual(collection)
    expect(access.batch).not.toHaveBeenCalled()
  })

  it("refuses a request above the size bound before it reaches the server", async () => {
    const { access, send, reply } = connect(true)
    send(1, "data.batch", { collection: "votes", documents: [{ id: "big", ifVersion: 0, value: { text: "x".repeat(1024 * 1024 + 8192) } }] })
    expect(await reply(1)).toMatchObject({ code: "limit" })
    expect(access.batch).not.toHaveBeenCalled()
  })

  it.each([
    ["an undeclared collection", { collection: "secrets" }, "data.get"],
    ["a collection path", { collection: "../votes" }, "data.get"],
    ["an extra parameter", { collection: "votes", url: "https://example.net" }, "data.get"],
    ["too many writes", { collection: "votes", documents: Array.from({ length: 51 }, (_, i) => ({ id: `d${i}`, ifVersion: 0, value: {} })) }, "data.batch"],
    ["a missing ifVersion", { collection: "votes", documents: [{ id: "yes", value: {} }] }, "data.batch"],
    ["a negative ifVersion", { collection: "votes", documents: [{ id: "yes", ifVersion: -1, value: {} }] }, "data.batch"],
    ["an array value", { collection: "votes", documents: [{ id: "yes", ifVersion: 0, value: [] }] }, "data.batch"],
    ["a duplicate document", { collection: "votes", documents: [{ id: "yes", ifVersion: 0, value: {} }, { id: "yes", ifVersion: 0, value: {} }] }, "data.batch"],
    ["an extra document key", { collection: "votes", documents: [{ id: "yes", ifVersion: 0, value: {}, actor: "owner" }] }, "data.batch"],
  ])("refuses %s", async (_, params, method) => {
    const { access, send, reply } = connect(true)
    send(1, method, params)
    expect((await reply(1)).error).toEqual(expect.any(String))
    expect(access.calls).toEqual([])
  })

  it("refuses a request that carries a port and ignores malformed envelopes", async () => {
    const { access, send, reply, replies, channel } = connect(true)
    const smuggled = new MessageChannel()
    open.push(smuggled)
    send(1, "data.get", { collection: "votes" }, {}, [smuggled.port1])
    expect(await reply(1)).toMatchObject({ error: "Requests cannot carry ports." })
    send(2, "data.get", { collection: "votes" }, { token: "x" })
    expect(await reply(2)).toMatchObject({ error: "Unsupported request." })
    channel.port2.postMessage({ type: "crewship.pages.request/v1", id: 3, method: "runAction", params: {} })
    channel.port2.postMessage({ type: INTERACTIVE_REQUEST, id: 0, method: "viewer", params: {} })
    channel.port2.postMessage("viewer")
    await new Promise(resolve => setTimeout(resolve, 30))
    expect([...replies.keys()]).toEqual([1, 2])
    expect(access.calls).toEqual([])
  })

  it("bounds concurrent server requests", async () => {
    const access = fakeAccess()
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    access.get = vi.fn(async () => { await gate; return collection })
    const { send, reply } = connect(true, access)
    for (let id = 1; id <= INTERACTIVE_MAX_IN_FLIGHT + 1; id++) send(id, "data.get", { collection: "votes" })
    expect(await reply(INTERACTIVE_MAX_IN_FLIGHT + 1)).toMatchObject({ code: "limit" })
    release()
    expect((await reply(1)).result).toEqual(collection)
    expect(access.get).toHaveBeenCalledTimes(INTERACTIVE_MAX_IN_FLIGHT)
  })

  it("stops answering once closed", async () => {
    const { send, replies } = connect(true)
    brokers[0].close()
    send(1, "viewer")
    await new Promise(resolve => setTimeout(resolve, 30))
    expect(replies.size).toBe(0)
  })
})

describe("createInteractiveDataAccess", () => {
  const ok = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } })
  it("uses the existing data endpoints with the viewer's session and projects reader-safe fields", async () => {
    const fetcher = vi.fn(async (_input: string, _init?: RequestInit) => ok({ collection: "votes", code_version: 3, documents: [{ id: "yes", version: 2, value: { count: 2 }, updated_at: "2026-10-31T10:00:00Z", source_label: "You", source_kind: "human", updated_by: "user_secret" }] }))
    const access = createInteractiveDataAccess({ fetcher, workspaceId: "ws1", pageId: "pg1", codeVersion: 3, viewerName: () => "Ada" })
    const read = await access.get("votes", new AbortController().signal)
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/pages/%40pg1/artifact/data/votes?workspace_id=ws1&version=3")
    expect(read).toEqual({ collection: "votes", documents: [{ id: "yes", version: 2, value: { count: 2 }, updated_at: "2026-10-31T10:00:00Z", source_label: "You" }] })
    await access.batch("votes", [{ id: "yes", ifVersion: 2, value: { count: 3 } }], new AbortController().signal)
    expect(fetcher.mock.calls[1][0]).toBe("/api/v1/pages/%40pg1/artifact/data/votes?workspace_id=ws1")
    expect(fetcher.mock.calls[1][1]).toMatchObject({ method: "POST", body: JSON.stringify({ base_version: 3, documents: [{ id: "yes", ifVersion: 2, value: { count: 3 } }] }) })
    expect(access.viewer()).toEqual({ name: "Ada" })
  })

  it.each([
    [409, { code: "artifact_data_conflict" }, "conflict"],
    [403, { error: "forbidden" }, "forbidden"],
    [413, { code: "artifact_data_quota" }, "limit"],
    [500, {}, "unavailable"],
  ])("maps HTTP %i to %s", async (status, body, code) => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify(body), { status }))
    const access = createInteractiveDataAccess({ fetcher, workspaceId: "ws1", pageId: "pg1", codeVersion: 1, viewerName: () => "" })
    await expect(access.batch("votes", [{ id: "yes", ifVersion: 0, value: {} }], new AbortController().signal)).rejects.toMatchObject({ code })
  })
})

describe("interactive frame isolation rules", () => {
  it("requires a runtime on a separate site", () => {
    expect(() => validateInteractiveRuntime("https://studio.example.com/api/v1/pages/runtime/bootstrap", "https://studio.example.com/artifacts/x", false)).toThrow()
    expect(() => validateInteractiveRuntime("https://pages.example.net/api/v1/pages/other", "https://studio.example.com/", false)).toThrow()
    expect(() => validateInteractiveRuntime("https://pages.example.net/api/v1/pages/runtime/bootstrap", "https://studio.example.com/", false)).not.toThrow()
  })

  it("hands the port only to the first document and ends on navigation", () => {
    const posted: unknown[][] = []
    const frame = { postMessage: (...args: unknown[]) => posted.push(args) } as unknown as Window
    const session = new InteractiveFrameSession(interactiveArtifactSource([{ path: "index.html", media_type: "text/html", content: "<p>Hi</p>" }], "index.html"), fakeAccess(), { collections: ["votes"], canWrite: false })
    expect(session.load(frame)).toBe(true)
    expect(posted).toHaveLength(1)
    expect(posted[0][0]).toMatchObject({ type: "crewship.pages.boot/v1", artifact: { format: "crewship-page-preview/v1" } })
    expect(posted[0][1]).toBe("*")
    expect(session.load(frame)).toBe(false)
    expect(posted).toHaveLength(1)
    ;(posted[0][2] as MessagePort[])[0].close()
  })

  it("refuses screens outside the manifest and oversized sources", () => {
    expect(() => interactiveArtifactSource([], "index.html")).toThrow()
    expect(() => interactiveArtifactSource([{ path: "index.html", media_type: "text/html", content: "x".repeat(2 * 1024 * 1024) }], "index.html")).toThrow(/2 MiB/)
  })
})
