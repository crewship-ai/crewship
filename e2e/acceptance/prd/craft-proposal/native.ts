// Native Craft (Word/Sheet) API helpers for the craft-proposal scenario.
// Every call goes through the public HTTP API with the caller's own cookies;
// session tokens stay in memory and are never written to evidence.
import type { APIRequestContext } from "@playwright/test"
import { createHash, randomUUID } from "node:crypto"
import { readFileSync } from "node:fs"
import { emptyGridCraftNativeDocument } from "../../../../lib/mooring/gridcraft-empty"
import { trackPage } from "../created"

export type NativeApp = "wordcraft" | "gridcraft"

export interface HttpResult<T = Record<string, unknown>> {
  status: number
  body: T
}

/** The blank documents the product's own "new Word/Sheet" flow initializes with. */
export function emptyNativeDocument(app: NativeApp): Buffer {
  if (app === "gridcraft") return Buffer.from(emptyGridCraftNativeDocument())
  const raw = readFileSync(new URL("../../../../lib/mooring/wordcraft-native-empty.json", import.meta.url), "utf8")
  return Buffer.from(JSON.stringify(JSON.parse(raw)))
}

export const sha256 = (b: Buffer | Uint8Array): string => "sha256:" + createHash("sha256").update(b).digest("hex")

async function json<T>(res: { status(): number; json(): Promise<unknown>; text(): Promise<string> }): Promise<HttpResult<T>> {
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = { text: (await res.text().catch(() => "")).slice(0, 300) }
  }
  return { status: res.status(), body: body as T }
}

export interface Admission {
  token: string
  id: string
}

export interface Head {
  version: string
  hash: string
  doc: Record<string, unknown>
}

export interface ProposalView {
  id: string
  author: {
    actor_id: string
    display_name: string
    kind: string
    agent_id?: string
  }
  base_version: number
  summary: string
  created_at: string
  state: string
  result_version?: number
}

/** One person's view of one native Page in one workspace. */
export class NativePage {
  constructor(
    readonly api: APIRequestContext,
    readonly workspaceId: string,
    readonly pageId: string,
  ) {}

  private url(rest: string): string {
    return `/api/v1/pages/${encodeURIComponent("@" + this.pageId)}/craft/${rest}${rest.includes("?") ? "&" : "?"}workspace_id=${encodeURIComponent(this.workspaceId)}`
  }

  private headers(a?: Admission): Record<string, string> {
    return a ? { "X-Craft-Session": a.token, "Cache-Control": "no-cache" } : {}
  }

  async availability(): Promise<
    HttpResult<{
      enabled?: boolean
      can_edit?: boolean
      agents_enabled?: boolean
    }>
  > {
    return json(await this.api.get(this.url("availability")))
  }

  async admit(): Promise<
    HttpResult<{ session_token?: string; session_id?: string }> & {
      admission?: Admission
    }
  > {
    const r = await json<{ session_token?: string; session_id?: string }>(await this.api.post(this.url("sessions"), { data: {} }))
    const admission = r.body.session_token && r.body.session_id ? { token: r.body.session_token, id: r.body.session_id } : undefined
    return { ...r, admission }
  }

  /** A fresh admission or a failed assertion with the status. */
  async session(): Promise<Admission> {
    const r = await this.admit()
    if (r.status !== 201 || !r.admission) throw new Error(`native session: HTTP ${r.status}`)
    return r.admission
  }

  async lease(a: Admission): Promise<
    HttpResult<{
      holder?: {
        kind?: string
        actor_id?: string
        display_name?: string
      } | null
      generation?: string
      mine?: boolean
      can_write?: boolean
    }>
  > {
    return json(await this.api.get(this.url("lease"), { headers: this.headers(a) }))
  }

  async acquire(a: Admission): Promise<
    HttpResult<{
      generation?: string
      mine?: boolean
      holder?: { kind?: string } | null
      error?: string
    }>
  > {
    return json(
      await this.api.post(this.url("lease/acquire"), {
        data: {},
        headers: this.headers(a),
      }),
    )
  }

  async release(a: Admission, generation: string): Promise<HttpResult> {
    return json(
      await this.api.post(this.url("lease/release"), {
        data: { generation },
        headers: this.headers(a),
      }),
    )
  }

  async head(a?: Admission): Promise<Head> {
    const admission = a ?? (await this.session())
    const r = await json<{ version: string; bytes: string }>(
      await this.api.get(this.url("document"), {
        headers: this.headers(admission),
      }),
    )
    if (r.status !== 200) throw new Error(`native document read: HTTP ${r.status}`)
    const raw = Buffer.from(r.body.bytes, "base64")
    return {
      version: String(r.body.version),
      hash: sha256(raw),
      doc: JSON.parse(raw.toString("utf8")) as Record<string, unknown>,
    }
  }

  async proposals(a: Admission): Promise<HttpResult<{ proposals?: ProposalView[]; error?: string }>> {
    return json(await this.api.get(this.url("proposals"), { headers: this.headers(a) }))
  }

  async proposal(
    a: Admission,
    id: string,
  ): Promise<{
    status: number
    proposal?: ProposalView
    contentHash?: string
    computedHash?: string
    doc?: Record<string, unknown>
  }> {
    const r = await json<{
      proposal?: ProposalView
      bytes?: string
      content_hash?: string
    }>(
      await this.api.get(this.url(`proposals/${encodeURIComponent(id)}`), {
        headers: this.headers(a),
      }),
    )
    if (r.status !== 200 || !r.body.bytes) return { status: r.status }
    const raw = Buffer.from(r.body.bytes, "base64")
    return {
      status: r.status,
      proposal: r.body.proposal,
      contentHash: r.body.content_hash,
      computedHash: sha256(raw),
      doc: JSON.parse(raw.toString("utf8")) as Record<string, unknown>,
    }
  }

  async accept(
    a: Admission,
    id: string,
    generation: string,
  ): Promise<
    HttpResult<{
      proposal?: ProposalView
      receipt?: { version?: string; sessionId?: string }
      error?: string
    }>
  > {
    return json(
      await this.api.post(this.url(`proposals/${encodeURIComponent(id)}/accept`), { data: { sessionId: a.id, generation }, headers: this.headers(a) }),
    )
  }

  async reject(a: Admission, id: string): Promise<HttpResult<{ state?: string; error?: string }>> {
    return json(await this.api.post(this.url(`proposals/${encodeURIComponent(id)}/reject`), { data: {}, headers: this.headers(a) }))
  }

  /** Native agent task, the API behind `crewship craft task start --whole-document`. */
  async startTask(chatId: string, messageId: string): Promise<HttpResult<{ work_id?: string; status?: string; error?: string }>> {
    return json(
      await this.api.post(this.url("tasks"), {
        data: { chat_id: chatId, message_id: messageId, whole_document: true },
      }),
    )
  }

  async task(workId: string): Promise<HttpResult<TaskStatus>> {
    return json(await this.api.get(this.url(`tasks/${encodeURIComponent(workId)}`)))
  }

  editorPath(): string {
    return (
      "/pages/craft/editor?" +
      new URLSearchParams({
        workspace_id: this.workspaceId,
        page_id: this.pageId,
      }).toString()
    )
  }
}

export interface TaskStatus {
  state?: string
  run_id?: string
  operation_inflight?: boolean
  events?: Array<{
    operation?: string
    status?: number
    holder_kind?: string
    proposal_id?: string
    error?: string
  }>
  last_committed_receipt?: {
    version?: string
    sessionId?: string
    contentHash?: string
  } | null
}

export interface CreatedNativePage {
  page: NativePage
  slug: string
  initVersion: string
  /** The creating admission; it holds the initial lease (generation `initGeneration`). */
  creator: Admission
  initGeneration: string
}

/**
 * Creates a test Page and initializes it as a native Word/Sheet document.
 * Returns null with the HTTP status when the origin has no native engines
 * (initialization answers 503), so callers can skip with that reason.
 */
export async function createNativePage(
  api: APIRequestContext,
  workspaceId: string,
  app: NativeApp,
  name: string,
  opts: { privateAudience?: boolean } = {},
): Promise<{ created: CreatedNativePage } | { unavailable: number; reason: string }> {
  const slug = `prd-qa-${app}-${randomUUID().replaceAll("-", "").slice(0, 10)}`
  const q = `workspace_id=${encodeURIComponent(workspaceId)}`
  const c = await json<{ id?: string; error?: string }>(
    await api.post(`/api/v1/pages?${q}`, {
      data: {
        name,
        slug,
        panels: [],
        allow_empty: true,
        created_by_test: true,
        ...(opts.privateAudience ? { private_audience: true } : {}),
      },
    }),
  )
  if (c.status !== 201 || !c.body.id) throw new Error(`create test page: HTTP ${c.status} ${JSON.stringify(c.body).slice(0, 200)}`)
  trackPage(workspaceId, c.body.id)
  const page = new NativePage(api, workspaceId, c.body.id)
  const init = await json<{
    session_token?: string
    session_id?: string
    generation?: string
    version?: string
    error?: string
  }>(
    await api.post(`/api/v1/pages/${encodeURIComponent("@" + c.body.id)}/craft/initialize?${q}`, {
      data: {
        application_id: app,
        bytes: emptyNativeDocument(app).toString("base64"),
      },
      timeout: 120_000,
    }),
  )
  if (init.status === 503) {
    await deletePage(api, workspaceId, c.body.id)
    return {
      unavailable: init.status,
      reason: String(init.body.error ?? "native documents unavailable"),
    }
  }
  if (init.status !== 201 || !init.body.session_token || !init.body.session_id || !init.body.generation) {
    throw new Error(`initialize ${app}: HTTP ${init.status} ${JSON.stringify(init.body).slice(0, 200)}`)
  }
  return {
    created: {
      page,
      slug,
      initVersion: String(init.body.version),
      creator: { token: init.body.session_token, id: init.body.session_id },
      initGeneration: init.body.generation,
    },
  }
}

export async function deletePage(api: APIRequestContext, workspaceId: string, pageId: string): Promise<number> {
  const res = await api.delete(`/api/v1/pages/${encodeURIComponent("@" + pageId)}?workspace_id=${encodeURIComponent(workspaceId)}`)
  return res.status()
}

/** Plain text of a WordCraft document body. */
export function wordText(doc: Record<string, unknown>): string {
  const body = (doc.body ?? []) as Array<{ text?: unknown }>
  return body.map((b) => (typeof b.text === "string" ? b.text : "")).join("\n")
}

/** Value of one GridCraft cell on the first sheet (0-based row/col). */
export function sheetCell(doc: Record<string, unknown>, row: number, col: number): string | undefined {
  const sheets = doc.sheets as
    | Array<{
        cells?: {
          bands?: Record<string, { rows?: Record<string, Record<string, { value?: unknown }>> }>
        }
      }>
    | undefined
  const cell = sheets?.[0]?.cells?.bands?.["0"]?.rows?.[String(row)]?.[String(col)]
  const v = cell?.value
  if (typeof v === "string") return v
  if (v && typeof v === "object" && "v" in v) return String((v as { v: unknown }).v)
  return undefined
}
