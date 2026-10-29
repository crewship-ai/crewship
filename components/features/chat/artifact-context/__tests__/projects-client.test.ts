import { afterEach, describe, expect, it, vi } from "vitest"
import {
  ArtifactRequestError, createArtifactComment, fetchArtifactAvailability, fetchArtifactProject, parseArtifactProject, parseArtifactComment,
  restoreArtifactVersion, shareArtifactVersion,
} from "../projects/client"

afterEach(() => vi.restoreAllMocks())
const respond = (status: number, body?: unknown, type = "application/json") =>
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(body === undefined ? null : typeof body === "string" ? body : JSON.stringify(body), { status, headers: { "content-type": type } }))

const snapshot = {
  page_id: "p1", can_write: true, latest_version: 3, shared_version: 2,
  manifest: { version: 3, entry_point: "index.html", files: [{ path: "index.html", content: "<h1 id=a>Hi</h1>", media_type: "text/html" }] },
  versions: [{ version: 3 }, { version: 2, entrypoint: "index.html", restored_from: 1 }],
}

async function rejection(promise: Promise<unknown>): Promise<ArtifactRequestError> {
  const error = await promise.then(() => null, (e: unknown) => e)
  expect(error).toBeInstanceOf(ArtifactRequestError)
  return error as ArtifactRequestError
}

describe("artifact project client", () => {
  it("preserves only an exact selected Slides classification without guessing from names", () => {
    expect(parseArtifactProject({ ...snapshot, artifact_kind: "slides" })?.artifact_kind).toBe("slides")
    for (const kind of ["Slides", "unsupported", true, null]) expect(parseArtifactProject({ ...snapshot, name: "Slides", artifact_kind: kind })?.artifact_kind).toBeUndefined()
  })
  it("reads the snapshot with the workspace, an encoded slug and no cache", async () => {
    const spy = respond(200, snapshot)
    await fetchArtifactProject("w 1", "a/b", 2)
    const [url, init] = spy.mock.calls[0]
    expect(String(url)).toBe("/api/v1/pages/a%2Fb/artifact?workspace_id=w+1&version=2")
    expect((init as RequestInit).cache).toBe("no-store")
    expect((init as RequestInit).method ?? "GET").toBe("GET")
  })

  it("posts JSON for restore, share and comments", async () => {
    const spy = respond(200, { version: 4, actor: "u" })
    expect(await restoreArtifactVersion("w", "s", { base_version: 3, version: 1 })).toEqual({ version: 4, actor: "u" })
    const [url, init] = spy.mock.calls[0]
    expect(String(url)).toBe("/api/v1/pages/s/artifact/restore?workspace_id=w")
    expect((init as RequestInit).method).toBe("POST")
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({ base_version: 3, version: 1 })
    expect(new Headers((init as RequestInit).headers).get("content-type")).toBe("application/json")

    respond(200, { shared_version: 3, url: "https://x" })
    expect(await shareArtifactVersion("w", "s", { base_version: 3, version: 3 })).toEqual({ shared_version: 3, url: "https://x" })

    const comment = { id: "c1", version: 3, path: "index.html", selector: "#a", quote: "Hi", text: "Bigger", status: "pending" }
    const created = respond(201, comment)
    expect(await createArtifactComment("w", "s", { version: 3, path: "index.html", selector: "#a", quote: "Hi", text: "Bigger", send_to_agent: true })).toEqual(comment)
    expect(String(created.mock.calls.at(-1)![0])).toBe("/api/v1/pages/s/artifact/comments?workspace_id=w")
  })

  it("availability is true only for an explicit enabled answer", async () => {
    const spy = respond(200, { enabled: true })
    expect(await fetchArtifactAvailability("w")).toBe(true)
    expect(String(spy.mock.calls[0][0])).toBe("/api/v1/pages/artifacts/availability?workspace_id=w")
    respond(200, { enabled: "yes" })
    expect(await fetchArtifactAvailability("w")).toBe(false)
    respond(404, "404 page not found", "text/plain")
    expect(await fetchArtifactAvailability("w")).toBe(false)
  })

  it.each([
    [409, { error: "Someone published a newer version", code: "artifact_version_conflict" }, "artifact_version_conflict", "Someone published a newer version"],
    [409, { error: "Anchor moved", code: "artifact_anchor_mismatch" }, "artifact_anchor_mismatch", "Anchor moved"],
    [413, { error: "History is full", code: "artifact_history_quota" }, "artifact_history_quota", "History is full"],
    [403, { error: "nope" }, "forbidden", "nope"],
    [404, "404 page not found", "not_found", undefined],
    [400, { code: "mystery" }, "invalid", undefined],
    [422, {}, "invalid", undefined],
    [503, {}, "unavailable", undefined],
  ] as const)("maps %i to an error code", async (status, body, code, message) => {
    respond(status, body, typeof body === "string" ? "text/plain" : "application/json")
    const error = await rejection(restoreArtifactVersion("w", "s", { base_version: 1, version: 1 }))
    expect(error.status).toBe(status)
    expect(error.code).toBe(code)
    if (message) expect(error.message).toBe(message)
    else expect(error.message.length).toBeGreaterThan(0)
  })

  it("a network failure is unavailable and a malformed body is invalid", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("network"))
    expect((await rejection(fetchArtifactProject("w", "s"))).code).toBe("unavailable")
    respond(200, "{not json", "application/json")
    expect((await rejection(fetchArtifactProject("w", "s"))).code).toBe("invalid")
    respond(200, { page_id: "p" })
    expect((await rejection(fetchArtifactProject("w", "s"))).code).toBe("invalid")
  })
})

describe("parseArtifactProject", () => {
  it("normalizes entry_point and defaults comments and versions", () => {
    const parsed = parseArtifactProject({ ...snapshot, versions: undefined })!
    expect(parsed.manifest.entrypoint).toBe("index.html")
    expect(parsed.comments).toEqual([])
    expect(parsed.versions).toEqual([])
  })

  it("normalizes entrypoint on version rows and keeps restored_from", () => {
    const parsed = parseArtifactProject(snapshot)!
    expect(parsed.versions[1]).toEqual({ version: 2, entry_point: "index.html", restored_from: 1 })
  })

  it("keeps proposed fields only when correctly typed", () => {
    const parsed = parseArtifactProject({
      ...snapshot, name: 42, slug: "deck",
      comments: [
        { id: "c1", version: 3, path: "index.html", selector: "#a", quote: "Hi", text: "x", status: "answered", answered_version: 4, replies: [{ text: "done", version: 4 }] },
        { id: "c2", version: 3, path: "index.html", selector: "", quote: "", text: "y", status: "bogus", answered_version: "4", replies: "no" },
        { id: 7, text: "dropped" },
      ],
      quota: { used_bytes: 10, limit_bytes: 100, warning: false },
    })!
    expect(parsed.name).toBeUndefined()
    expect(parsed.slug).toBe("deck")
    expect(parsed.comments).toHaveLength(2)
    expect(parsed.comments[0]).toMatchObject({ status: "answered", answered_version: 4, replies: [{ text: "done", version: 4 }] })
    expect(parsed.comments[1].status).toBeUndefined()
    expect(parsed.comments[1].answered_version).toBeUndefined()
    expect(parsed.comments[1].replies).toBeUndefined()
    expect(parsed.quota).toEqual({ used_bytes: 10, limit_bytes: 100, warning: false })
  })

  it("rejects snapshots missing required fields", () => {
    expect(parseArtifactProject(null)).toBeNull()
    expect(parseArtifactProject({ ...snapshot, can_write: "true" })).toBeNull()
    expect(parseArtifactProject({ ...snapshot, manifest: { version: 1, files: [] } })).toBeNull()
    expect(parseArtifactProject({ ...snapshot, manifest: { ...snapshot.manifest, files: [{ path: "a" }] } })).toBeNull()
  })
})

 it.each([true,false,"true",1,null,undefined])("preserves only boolean retry eligibility (%s)",value=>{
 const parsed=parseArtifactComment({id:"c1",version:1,path:"index.html",text:"Waiting",delivery_retryable:value})!
 expect(parsed.delivery_retryable).toBe(typeof value==="boolean"?value:undefined)
 })

it("preserves explicit owner capability booleans and drops truthy or missing grants",()=>{
 expect(parseArtifactProject({...snapshot,can_share:true,can_restore:false,can_send_to_agent:true,can_comment:true})).toMatchObject({can_share:true,can_restore:false,can_send_to_agent:true,can_comment:true})
 const parsed=parseArtifactProject({...snapshot,can_share:"true",can_restore:1,can_send_to_agent:null})
 expect(parsed?.can_share).not.toBe(true);expect(parsed?.can_restore).not.toBe(true);expect(parsed?.can_send_to_agent).not.toBe(true)
})
