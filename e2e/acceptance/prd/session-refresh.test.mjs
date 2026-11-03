// node --test e2e/acceptance/prd/session-refresh.test.mjs
// (also run by e2e/acceptance/prd/report.test.mjs, the PRD runner step in CI)
import assert from "node:assert/strict"
import { test } from "node:test"
import { REFRESH_PATH, sessionExpired, withSessionRenewal } from "./session-refresh.ts"

const reply = (status, reason) => ({ status: () => status, headers: () => (reason ? { "www-authenticate": `Bearer error="${reason}"` } : {}), text: async () => "" })

/**
 * A fake APIRequestContext whose access cookie expires: after `expireAfter`
 * calls every API call answers 401 no_credentials until the session is
 * refreshed (`refresh` decides the refresh status).
 */
function fakeContext(name, { expireAfter = Infinity, refresh = 200 } = {}) {
  const calls = []
  let left = expireAfter
  let disposed = false
  const answer = (method, url, opts) => {
    calls.push({ method, url, headers: opts?.headers })
    if (url === REFRESH_PATH) {
      if (refresh === 200) left = Infinity
      return reply(refresh, refresh === 200 ? undefined : "session_revoked")
    }
    if (left <= 0) return reply(401, "no_credentials")
    left--
    return reply(201)
  }
  return {
    name,
    calls,
    get disposed() {
      return disposed
    },
    get: async (url, opts) => answer("GET", url, opts),
    post: async (url, opts) => answer("POST", url, opts),
    dispose: async () => void (disposed = true),
    storageState: async () => ({ cookies: [], origins: [] }),
  }
}

test("sessionExpired: only a 401 for a missing or expired session cookie", () => {
  assert.equal(sessionExpired(reply(401, "no_credentials")), true)
  assert.equal(sessionExpired(reply(401, "session_expired")), true)
  assert.equal(sessionExpired(reply(401, "session_revoked")), false)
  assert.equal(sessionExpired(reply(401)), false)
  assert.equal(sessionExpired(reply(403, "no_credentials")), false)
  assert.equal(sessionExpired(reply(201)), false)
})

test("without renewal a context built from a login snapshot answers 401 once the access cookie expired (G3 RC-3 hour-sheet)", async () => {
  const ctx = fakeContext("snapshot", { expireAfter: 2 })
  assert.equal((await ctx.post("/api/v1/x/sessions")).status(), 201)
  assert.equal((await ctx.post("/api/v1/x/sessions")).status(), 201)
  assert.equal((await ctx.post("/api/v1/x/sessions")).status(), 401)
})

test("a context that owns its login refreshes once and repeats the call", async () => {
  const ctx = fakeContext("owner", { expireAfter: 1 })
  const log = []
  const api = withSessionRenewal(ctx, "https://app.example.test/", { log })
  assert.equal((await api.post("/api/v1/x/sessions", { data: {} })).status(), 201)
  assert.equal((await api.post("/api/v1/x/sessions", { data: {} })).status(), 201)
  assert.deepEqual(
    ctx.calls.map((c) => c.url),
    ["/api/v1/x/sessions", "/api/v1/x/sessions", REFRESH_PATH, "/api/v1/x/sessions"],
  )
  assert.deepEqual(ctx.calls[2].headers, { Origin: "https://app.example.test" })
  assert.deepEqual(log.map((l) => [l.how, l.status]), [["refresh", 200]])
})

test("concurrent calls on an expired session share one refresh", async () => {
  const ctx = fakeContext("owner", { expireAfter: 0 })
  const api = withSessionRenewal(ctx, "https://h.example")
  const res = await Promise.all([api.get("/a"), api.get("/b"), api.post("/c")])
  assert.deepEqual(res.map((r) => r.status()), [201, 201, 201])
  assert.equal(ctx.calls.filter((c) => c.url === REFRESH_PATH).length, 1)
})

test("a context sharing its login with a browser never refreshes that session: it moves to its own login once", async () => {
  const shared = fakeContext("shared", { expireAfter: 1 })
  const own = fakeContext("own", { expireAfter: 1 })
  let logins = 0
  const log = []
  const api = withSessionRenewal(shared, "https://h.example", { log, relogin: async () => (logins++, own) })
  assert.equal((await api.get("/a")).status(), 201)
  assert.equal((await api.get("/b")).status(), 201) // shared expired -> relogin -> repeated on own
  assert.equal((await api.get("/c")).status(), 201) // own expired -> refresh of own session
  assert.equal(logins, 1)
  assert.equal(shared.calls.filter((c) => c.url === REFRESH_PATH).length, 0, "the browser's session is never refreshed by the harness")
  assert.deepEqual(own.calls.map((c) => c.url), ["/b", "/c", REFRESH_PATH, "/c"])
  assert.deepEqual(log.map((l) => l.how), ["relogin", "refresh"])
  await api.dispose()
  assert.equal(shared.disposed && own.disposed, true)
})

test("a failed renewal, another 401, and /api/auth/ calls reach the caller unchanged", async () => {
  const revoked = fakeContext("owner", { expireAfter: 0, refresh: 401 })
  const api = withSessionRenewal(revoked, "https://h.example")
  assert.equal((await api.get("/a")).status(), 401)
  assert.equal(revoked.calls.filter((c) => c.url === "/a").length, 1, "not repeated after a failed refresh")

  const failingLogin = withSessionRenewal(fakeContext("shared", { expireAfter: 0 }), "https://h.example", { relogin: async () => Promise.reject(new Error("429")) })
  assert.equal((await failingLogin.get("/a")).status(), 401)

  const ctx = fakeContext("owner", { expireAfter: 0 })
  const auth = withSessionRenewal(ctx, "https://h.example")
  assert.equal((await auth.get("/api/auth/session")).status(), 401)
  assert.equal(ctx.calls.length, 1)
})

test("non-call members pass through bound to the current context", async () => {
  const ctx = fakeContext("owner")
  const api = withSessionRenewal(ctx, "https://h.example")
  assert.deepEqual(await api.storageState(), { cookies: [], origins: [] })
  assert.equal(api.name, "owner")
})
