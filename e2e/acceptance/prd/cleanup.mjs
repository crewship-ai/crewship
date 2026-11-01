// Deletes the objects this harness created and left behind, by name pattern:
//   workspaces  slug ^prd-qa-<label>-<10 hex>$  AND  name starting "PRD acceptance "
//   pages       slug ^prd-                      AND  name starting "PRD "
//   (Designs and Projects are pages)
// Pages are looked for in the workspaces of PRD_AGENT_WORKSPACE_ID /
// PRD_WORKSPACE_ID and in every harness workspace that is kept. Anything that
// matches only one of the two rules, or is younger than --older-than minutes
// (it may belong to a run in progress), is reported and left alone.
// Used by `run.mjs --cleanup [--dry-run] [--older-than MIN]`.

const WORKSPACE_SLUG = /^prd-qa-[a-z0-9-]*-[0-9a-f]{10}$/
const WORKSPACE_NAME = /^PRD acceptance /
const PAGE_SLUG = /^prd-/
const PAGE_NAME = /^PRD /

export function isHarnessWorkspace(w) {
  return WORKSPACE_SLUG.test(w.slug ?? "") && WORKSPACE_NAME.test(w.name ?? "")
}
export function isHarnessPage(p) {
  return PAGE_SLUG.test(p.slug ?? "") && PAGE_NAME.test(p.name ?? "")
}

/** Minimal cookie jar over fetch (no secrets are logged). */
class Session {
  constructor(origin) {
    this.origin = origin
    this.jar = new Map()
  }
  store(res) {
    for (const line of res.headers.getSetCookie?.() ?? []) {
      const [pair] = line.split(";")
      const eq = pair.indexOf("=")
      if (eq > 0) this.jar.set(pair.slice(0, eq).trim(), pair.slice(eq + 1))
    }
  }
  async call(method, path, body) {
    const headers = { cookie: [...this.jar].map(([k, v]) => `${k}=${v}`).join("; ") }
    if (body !== undefined) headers["content-type"] = "application/json"
    const res = await fetch(this.origin + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
    this.store(res)
    return res
  }
  async login(account) {
    const csrf = await this.call("GET", "/api/auth/csrf")
    const { csrfToken } = await csrf.json()
    const res = await this.call("POST", "/api/auth/callback/credentials", { email: account.email, password: account.password, csrfToken, redirect: "false" })
    const body = await res.json().catch(() => ({}))
    // Never echo the account.
    if (!res.ok || body.error) throw new Error(`cleanup: login failed (${res.status} ${body.error ?? ""})`.trim())
  }
}

const ageMinutes = (iso) => (iso ? (Date.now() - Date.parse(iso)) / 60_000 : Infinity)

/**
 * @param {{origin: string, owner: {email: string, password: string}, workspaceIds?: string[], olderThanMin?: number, dryRun?: boolean, log?: (s: string) => void}} o
 */
export async function cleanup(o) {
  const log = o.log ?? console.log
  const olderThan = o.olderThanMin ?? 60
  const s = new Session(o.origin)
  await s.login(o.owner)
  const result = { workspacesDeleted: 0, pagesDeleted: 0, skipped: [], failed: [] }
  const skip = (kind, id, why) => {
    result.skipped.push({ kind, id, why })
    log(`  skip ${kind} ${id}: ${why}`)
  }

  const wsRes = await s.call("GET", "/api/v1/workspaces")
  if (!wsRes.ok) throw new Error(`cleanup: list workspaces: HTTP ${wsRes.status}`)
  const raw = await wsRes.json()
  const workspaces = Array.isArray(raw) ? raw : (raw.workspaces ?? [])
  const harnessWs = []
  for (const w of workspaces) {
    const slugOk = WORKSPACE_SLUG.test(w.slug ?? "")
    const nameOk = WORKSPACE_NAME.test(w.name ?? "")
    if (!slugOk && !nameOk) continue
    if (!(slugOk && nameOk)) {
      skip("workspace", w.id, "matches only one of the slug and name patterns")
      continue
    }
    harnessWs.push(w)
  }

  // 1. Pages in the shared workspaces (agent workspace etc.), then 2. whole QA workspaces.
  const q = (id) => `workspace_id=${encodeURIComponent(id)}`
  for (const wsId of new Set(o.workspaceIds ?? [])) {
    const res = await s.call("GET", `/api/v1/pages?${q(wsId)}&test_items=include&archived=include`)
    if (!res.ok) {
      result.failed.push({ kind: "list pages", id: wsId, status: res.status })
      continue
    }
    for (const p of await res.json()) {
      const slugOk = PAGE_SLUG.test(p.slug ?? "")
      const nameOk = PAGE_NAME.test(p.name ?? "")
      if (!slugOk && !nameOk) continue
      if (!(slugOk && nameOk)) {
        skip("page", p.id, "matches only one of the slug and name patterns")
        continue
      }
      if (ageMinutes(p.created_at) < olderThan) {
        skip("page", p.id, `younger than ${olderThan} min`)
        continue
      }
      log(`  ${o.dryRun ? "would delete" : "delete"} page ${p.id} ${p.slug}`)
      if (o.dryRun) continue
      const del = await s.call("DELETE", `/api/v1/pages/${encodeURIComponent("@" + p.id)}?${q(wsId)}`)
      if (del.status < 300 || del.status === 404) result.pagesDeleted++
      else result.failed.push({ kind: "page", id: p.id, status: del.status })
    }
  }
  for (const w of harnessWs) {
    const info = await s.call("GET", `/api/v1/workspaces/${encodeURIComponent(w.id)}`)
    const created = info.ok ? ((await info.json().catch(() => ({}))).created_at ?? w.created_at) : w.created_at
    if (ageMinutes(created) < olderThan) {
      skip("workspace", w.id, `younger than ${olderThan} min (may belong to a running test)`)
      continue
    }
    log(`  ${o.dryRun ? "would delete" : "delete"} workspace ${w.id} ${w.slug}`)
    if (o.dryRun) continue
    const del = await s.call("DELETE", `/api/v1/workspaces/${encodeURIComponent(w.id)}?${q(w.id)}`, { confirm_slug: w.slug })
    if (del.status < 300 || del.status === 404) result.workspacesDeleted++
    else result.failed.push({ kind: "workspace", id: w.id, status: del.status })
  }
  return result
}
