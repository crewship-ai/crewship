import { afterEach, describe, expect, it, vi } from "vitest"
import { createArtifactComment, fetchArtifactProject, restoreArtifactVersion, shareArtifactVersion, type ArtifactRequestError } from "../projects/client"
import { createArtifactMockServer } from "../projects/mock-server"

const html = (title: string) => `<main><h1 id="title">  ${title}  </h1><p id="lead">Lead <b>text</b></p><p id="dup"></p><p id="dup"></p></main>`
const file = (title: string) => [{ path: "index.html", content: html(title), media_type: "text/html" }]
const post = (body: unknown) => ({ method: "POST", body: JSON.stringify(body) })

afterEach(() => vi.restoreAllMocks())

function server(options: Partial<Parameters<typeof createArtifactMockServer>[0]> = {}) {
  const mock = createArtifactMockServer({ slug: "deck", name: "Deck", files: file("One"), ...options })
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => mock.handle(String(input), init) ?? new Response("unrouted", { status: 599 }))
  return mock
}

describe("artifact mock server", () => {
  it("ignores URLs that are not artifact routes", () => {
    expect(createArtifactMockServer({ slug: "deck", files: [] }).handle("/api/v1/pages/deck/document")).toBeNull()
  })

  it("serves the snapshot through the real client", async () => {
    server()
    const snap = await fetchArtifactProject("w", "deck")
    expect(snap).toMatchObject({ slug: "deck", name: "Deck", latest_version: 1, shared_version: 0, can_write: true })
    expect(snap.manifest.entrypoint).toBe("index.html")
  })

  it("restore appends a version that records its source", async () => {
    const mock = server()
    mock.agentPublish(file("Two"), "second")
    expect(await restoreArtifactVersion("w", "deck", { base_version: 2, version: 1 })).toEqual({ version: 3, actor: "you" })
    const snap = await fetchArtifactProject("w", "deck")
    expect(snap.latest_version).toBe(3)
    expect(snap.manifest.restored_from).toBe(1)
    expect(snap.manifest.files[0].content).toContain("One")
    expect(snap.versions.map(v => v.version)).toEqual([3, 2, 1])
    expect((await fetchArtifactProject("w", "deck", 2)).manifest.files[0].content).toContain("Two")
  })

  it("rejects a stale base with a version conflict", async () => {
    const mock = server()
    mock.agentPublish(file("Two"))
    const error = await shareArtifactVersion("w", "deck", { base_version: 1, version: 1 }).catch((e: ArtifactRequestError) => e)
    expect(error).toMatchObject({ status: 409, code: "artifact_version_conflict" })
    expect(mock.state.sharedVersion).toBe(0)
  })

  it("share pins the shared version", async () => {
    const mock = server()
    expect(await shareArtifactVersion("w", "deck", { base_version: 1, version: 1 })).toEqual({ shared_version: 1, url: "/p/deck" })
    expect(mock.snapshot().shared_version).toBe(1)
  })

  it("answers 413 once history is full", async () => {
    server({ maxVersions: 1 })
    const error = await restoreArtifactVersion("w", "deck", { base_version: 1, version: 1 }).catch((e: ArtifactRequestError) => e)
    expect(error).toMatchObject({ status: 413, code: "artifact_history_quota" })
  })

  it("stores an anchored comment as pending and the agent answers it with the next version", async () => {
    const mock = server()
    const comment = await createArtifactComment("w", "deck", { version: 1, path: "index.html", selector: "#lead", quote: "Lead text", text: "Shorter", send_to_agent: true })
    expect(comment).toMatchObject({ selector: "#lead", quote: "Lead text", status: "pending" })
    const version = mock.agentPublish(file("Two"), "shorter lead", "Done")
    const [answered] = (await fetchArtifactProject("w", "deck")).comments
    expect(answered).toMatchObject({ status: "answered", answered_version: version, replies: [{ text: "Done", version }] })
  })

  it.each([["#missing", "x"], ["#dup", ""], ["#title", "Wrong"], [".title", "One"]])("rejects anchor %s / %s", async (selector, quote) => {
    server()
    const error = await createArtifactComment("w", "deck", { version: 1, path: "index.html", selector, quote, text: "x", send_to_agent: false }).catch((e: ArtifactRequestError) => e)
    expect(error).toMatchObject({ status: 409, code: "artifact_anchor_mismatch" })
  })

  describe("anchor quote rule (same as the backend)", () => {
    const page = (body: string) => server({ files: [{ path: "index.html", content: `<body>${body}</body>`, media_type: "text/html" }] })
    const send = (selector: string, quote: string) => createArtifactComment("w", "deck", { version: 1, path: "index.html", selector, quote, text: "x", send_to_agent: false })
      .then(() => "ok", (e: ArtifactRequestError) => e.code)

    it("concatenates text nodes without inserting spaces between tags", async () => {
      page(`<div id="plan-team"><h3>Team</h3><p>$24</p></div>`)
      expect(await send("#plan-team", "Team$24")).toBe("ok")
      expect(await send("#plan-team", "Team $24")).toBe("artifact_anchor_mismatch")
    })
    it("skips script and style, decodes entities and counts nested tags of the same name", async () => {
      page(`<div id="box">Q&amp;A<script>var a = "<div>"</script><style>b{}</style><div>in</div> out</div><div>after</div>`)
      expect(await send("#box", "Q&Ain out")).toBe("ok")
    })
    it("accepts a 500-byte prefix only at exactly 500 bytes", async () => {
      const long = "é".repeat(400) // 800 bytes in UTF-8
      page(`<p id="long">${long}</p>`)
      expect(await send("#long", long)).toBe("ok")
      expect(await send("#long", "é".repeat(250))).toBe("ok")
      expect(await send("#long", "é".repeat(249))).toBe("artifact_anchor_mismatch")
    })
  })

  it("a comment not sent to the agent has no queue status", async () => {
    server()
    const comment = await createArtifactComment("w", "deck", { version: 1, path: "index.html", selector: "", quote: "", text: "Nice", send_to_agent: false })
    expect(comment.status).toBeUndefined()
  })

  it("read-only viewers get no history and cannot write", () => {
    const mock = createArtifactMockServer({ slug: "deck", files: file("One"), canWrite: false })
    expect(mock.snapshot().versions).toEqual([])
    expect(mock.handle("/api/v1/pages/deck/artifact/share", post({ base_version: 1, version: 1 }))!.status).toBe(403)
  })

  it("craft routes 404 until enabled, then follow the lease", async () => {
    const mock = createArtifactMockServer({ slug: "deck", files: [] })
    expect(mock.handle("/api/v1/pages/deck/craft/availability")!.status).toBe(404)
    mock.state.craftEnabled = true
    mock.setLease({ holder: { actor_id: "a", display_name: "Ada", kind: "agent" }, mine: false, generation: 2, can_write: true })
    expect(mock.handle("/api/v1/pages/deck/craft/lease", post({}))!.status).toBe(409)
    await mock.handle("/api/v1/pages/deck/craft/lease/request-handoff", post({}))!.json()
    expect(mock.state.lease.handoff_requested_by).toBe("You")
    mock.setLease({ holder: null, mine: false, generation: 3, can_write: true })
    expect(await mock.handle("/api/v1/pages/deck/craft/lease", post({}))!.json()).toMatchObject({ mine: true, generation: 4 })
  })
})
