// @vitest-environment node
import { webcrypto } from "node:crypto"
import { beforeEach, afterEach, expect, it, vi } from "vitest"
const server = vi.hoisted(() => ({ send: vi.fn() }))
vi.mock("./api-fetch", () => ({ apiFetch: server.send }))
import { beginLocalDraftIdentity, finishLocalDraftIdentity, createLocalDraftCipher, LocalDraftReadRevoked } from "./local-draft-seal"
const scope = { human: "alice", workspace: "w", page: "page" }
let current = "alice", readable = true
let wrapped: Map<string, { key: string; human: string; page: string }>
beforeEach(() => {
  vi.stubGlobal("crypto", webcrypto); current = "alice"; readable = true; wrapped = new Map(); beginLocalDraftIdentity("alice")
  server.send.mockReset().mockImplementation(async (url: string, init: RequestInit) => {
    const body = JSON.parse(String(init.body)), page = decodeURIComponent(url.split("/pages/@")[1].split("/")[0])
    if (body.human !== current) return Response.json({ code: "draft_identity_mismatch" }, { status: 403 })
    if (!readable) return Response.json({ code: "draft_read_revoked" }, { status: 403 })
    if (body.operation === "wrap") { const envelope = `v1:${crypto.randomUUID()}`; wrapped.set(envelope, { key: body.key, human: current, page }); return Response.json({ envelope }) }
    const found = wrapped.get(body.envelope)
    if (!found || found.human !== current || found.page !== page) return Response.json({}, { status: 403 })
    return Response.json({ key: found.key })
  })
})
afterEach(() => { beginLocalDraftIdentity(null); finishLocalDraftIdentity(null); vi.unstubAllGlobals() })
it("stores ciphertext without the private text and recovers only after fresh authorization", async () => {
  const a = await createLocalDraftCipher(scope), sealed = await a.seal("private unsaved text", "source")
  expect(sealed).not.toContain("private unsaved text")
  a.close(); const b = await createLocalDraftCipher(scope)
  await expect(b.open(sealed, "source")).resolves.toBe("private unsaved text")
  expect(server.send.mock.calls.map(([, init]) => JSON.parse(String(init.body)).operation)).toEqual(["wrap", "wrap", "unwrap"])
})
it("refuses cross-Page or cross-purpose ciphertext", async () => {
  const a = await createLocalDraftCipher(scope), sealed = await a.seal("private", "source")
  await expect(a.open(sealed, "comment")).rejects.toThrow()
  const other = await createLocalDraftCipher({ ...scope, page: "other" })
  await expect(other.open(sealed, "source")).rejects.toThrow()
})
it("does not release plaintext to a replacement identity or after known read loss", async () => {
  const a = await createLocalDraftCipher(scope), sealed = await a.seal("private", "source")
  beginLocalDraftIdentity("bob"); current = "bob"
  await expect(a.open(sealed, "source")).rejects.toThrow()
  finishLocalDraftIdentity("bob")
  beginLocalDraftIdentity("alice"); current = "alice"; readable = false
  await expect(createLocalDraftCipher(scope)).rejects.toBeInstanceOf(LocalDraftReadRevoked)
})
it.each([400, 401, 500, 503])("keeps unavailable encrypted data when recovery returns %s", async status => {
  const a = await createLocalDraftCipher(scope), sealed = await a.seal("private", "source")
  server.send.mockResolvedValueOnce(Response.json({}, { status }))
  await expect(a.open(sealed, "source")).rejects.toThrow()
  expect(sealed).toContain("crewship-local-draft-v1")
})
it("seals admitted offline edits until the retirement drain ends, and then discards RAM keys", async () => {
  const a = await createLocalDraftCipher(scope)
  beginLocalDraftIdentity(null)
  const sealed = await a.seal("last admitted change", "source")
  await expect(a.open(sealed, "source")).rejects.toThrow()
  finishLocalDraftIdentity(null)
  await expect(a.seal("late stale write", "source")).rejects.toThrow()
  expect(server.send).toHaveBeenCalledOnce()
})
it("never revives an old cipher when the same identity returns before an expiry drain finishes", async () => {
  const a = await createLocalDraftCipher(scope), sealed = await a.seal("private", "source")
  await expect(a.open(sealed, "source")).resolves.toBe("private") // cache the data key
  beginLocalDraftIdentity(null)
  beginLocalDraftIdentity("alice")
  readable = false
  finishLocalDraftIdentity(null) // superseded by the new login
  await expect(a.open(sealed, "source")).rejects.toThrow()
})

// #4823: the synchronous unload capture uses real AES-GCM keystream material.
it("seals an unload capture synchronously, without plaintext, once per keystream, and opens it only with fresh same-person authorization", async () => {
  const a = await createLocalDraftCipher(scope)
  expect(a.sealNow!("too early", "source")).toBeUndefined() // nothing prepared: the caller must warn
  await a.prepareInstant!(64)
  const text = "Poslední slova před odchodem ✓"
  const captured = a.sealNow!(text, "source")!
  expect(captured).toBeTruthy()
  expect(captured).not.toContain("Poslední")
  expect(JSON.parse(captured).format).toBe("crewship-local-draft-instant-v1")
  // Single use: the keystream is gone until a fresh one is prepared.
  const again = a.sealNow!("second capture", "source")
  if (again) expect(JSON.parse(again).iv).not.toBe(JSON.parse(captured).iv)
  a.close()
  const reopened = await createLocalDraftCipher(scope)
  await expect(reopened.open(captured, "source")).resolves.toBe(text)
  await expect(reopened.open(captured, "comment")).rejects.toThrow()
  const otherPage = await createLocalDraftCipher({ ...scope, page: "other" })
  await expect(otherPage.open(captured, "source")).rejects.toThrow()
  beginLocalDraftIdentity("bob"); current = "bob"
  await expect(reopened.open(captured, "source")).rejects.toThrow()
})
it("refuses an unload capture larger than the prepared keystream and after close", async () => {
  const a = await createLocalDraftCipher(scope)
  await a.prepareInstant!(16)
  expect(a.sealNow!("x".repeat(128 * 1024), "source")).toBeUndefined()
  a.close()
  expect(a.sealNow!("short", "source")).toBeUndefined()
})
