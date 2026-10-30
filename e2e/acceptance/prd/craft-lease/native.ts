// Native Word/Sheet editor lease protocol, driven over the public HTTP API the
// editor itself uses (/api/v1/pages/@<id>/craft/...). Admission tokens stay in
// memory; nothing here writes them to evidence.
import { createHash } from "node:crypto"
import * as fs from "node:fs"
import type { APIRequestContext, APIResponse } from "@playwright/test"
import { emptyGridCraftNativeDocument } from "../../../../lib/mooring/gridcraft-empty"

// lib/mooring/wordcraft-native-create.ts imports this JSON, which Playwright's
// ESM loader cannot import without an attribute; read it the same way instead.
const WORD_EMPTY = new URL("../../../../lib/mooring/wordcraft-native-empty.json", import.meta.url)
function createEmptyNativeWord(): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(JSON.parse(fs.readFileSync(WORD_EMPTY, "utf8"))))
}

export type NativeApplication = "wordcraft" | "gridcraft"

export const APPLICATIONS: ReadonlyArray<{ application: NativeApplication; label: "Word" | "Sheet"; frameTitle: string }> = [
  { application: "wordcraft", label: "Word", frameTitle: "WordCraft editor" },
  { application: "gridcraft", label: "Sheet", frameTitle: "GridCraft editor" },
]

/** The same blank documents the "New Doc" dialog initializes with. */
export function emptyDocument(application: NativeApplication): Buffer {
  return Buffer.from(application === "gridcraft" ? emptyGridCraftNativeDocument() : createEmptyNativeWord())
}

export interface Availability {
  enabled: boolean
  can_edit: boolean
  preview_enabled: boolean
  agents_enabled: boolean
}

export interface LeaseView {
  holder: { actor_id: string; display_name: string; kind: "human" | "agent" } | null
  mine: boolean
  generation: string | null
  can_edit: boolean
  can_write: boolean
  handoff_requested_by: Array<{ request_id: string; actor_id: string; display_name: string; kind: string }>
}

export interface NativeDocument {
  version: string
  bytes: string
  applicationId: string
  enginePin: string
}

/** One native admission (POST .../craft/sessions) of one person. */
export interface Admission {
  token: string
  sessionId: string
}

export interface SaveReceipt {
  version: string
  generation: string
  sessionId: string
  contentHash: string
  phase: string
}

export function sha256(bytes: Buffer): string {
  return "sha256:" + createHash("sha256").update(bytes).digest("hex")
}

async function json<T>(res: APIResponse, expected: number, what: string): Promise<T> {
  if (res.status() !== expected) {
    throw new Error(`${what}: expected HTTP ${expected}, got ${res.status()} ${(await res.text()).slice(0, 300)}`)
  }
  return (await res.json()) as T
}

/** A native page in a QA workspace, addressed the way the editor addresses it. */
export class NativePage {
  constructor(
    readonly workspaceId: string,
    readonly pageId: string,
    readonly application: NativeApplication,
  ) {}

  private url(path: string, extra?: Record<string, string>): string {
    const q = new URLSearchParams({ workspace_id: this.workspaceId, ...extra })
    return `/api/v1/pages/${encodeURIComponent("@" + this.pageId)}/craft/${path}?${q}`
  }

  get editorPath(): string {
    return `/pages/craft/editor?${new URLSearchParams({ workspace_id: this.workspaceId, page_id: this.pageId })}`
  }

  /** Creates an empty page; it is not native until initialize() succeeds. */
  static async create(owner: APIRequestContext, workspaceId: string, application: NativeApplication, name: string): Promise<NativePage> {
    const slug = `prd-lease-${application}-${Date.now().toString(36)}`
    const res = await owner.post(`/api/v1/pages?${new URLSearchParams({ workspace_id: workspaceId })}`, {
      data: { name, slug, panels: [], allow_empty: true },
    })
    const body = await json<{ id: string }>(res, 201, "create page")
    return new NativePage(workspaceId, body.id, application)
  }

  async availability(api: APIRequestContext): Promise<Availability> {
    return json<Availability>(await api.get(this.url("availability")), 200, "availability")
  }

  /** Raw initialize, so a disabled server's answer can be asserted. */
  initializeRaw(owner: APIRequestContext): Promise<APIResponse> {
    return owner.post(this.url("initialize"), {
      data: { application_id: this.application, bytes: emptyDocument(this.application).toString("base64") },
    })
  }

  /**
   * Initializes version 1 exactly like the New Doc dialog and releases the
   * creation custody, so the document starts free ("Volné"). Returns false
   * when the server has no native editor profile (HTTP 503).
   */
  async initialize(owner: APIRequestContext): Promise<boolean> {
    const res = await this.initializeRaw(owner)
    if (res.status() === 503) return false
    const init = await json<{ session_token: string; generation: string; version: string; application_id: string }>(res, 201, "initialize native document")
    if (init.application_id !== this.application || init.version !== "1") throw new Error("initialize returned an unexpected document")
    await json(await this.leaseOp(owner, { token: init.session_token, sessionId: "" }, "release", { generation: init.generation }), 200, "release creation lease")
    return true
  }

  sessionRaw(api: APIRequestContext): Promise<APIResponse> {
    return api.post(this.url("sessions"), { data: {} })
  }

  async session(api: APIRequestContext): Promise<Admission> {
    const body = await json<{ session_token: string; session_id: string }>(await this.sessionRaw(api), 201, "native admission")
    return { token: body.session_token, sessionId: body.session_id }
  }

  async lease(api: APIRequestContext, a: Admission): Promise<LeaseView> {
    return json<LeaseView>(await api.get(this.url("lease"), { headers: { "X-Craft-Session": a.token } }), 200, "read lease")
  }

  leaseOp(
    api: APIRequestContext,
    a: Admission,
    op: "acquire" | "renew" | "release" | "request-handoff" | "handoff",
    body: { generation?: string; request_id?: string } = {},
  ): Promise<APIResponse> {
    return api.post(this.url(`lease/${op}`), { data: body, headers: { "X-Craft-Session": a.token } })
  }

  documentRaw(api: APIRequestContext, a: Admission): Promise<APIResponse> {
    return api.get(this.url("document"), { headers: { "X-Craft-Session": a.token, "Cache-Control": "no-cache" } })
  }

  async document(api: APIRequestContext, a: Admission): Promise<NativeDocument> {
    return json<NativeDocument>(await this.documentRaw(api, a), 200, "read native document")
  }

  /**
   * Saves `bytes` on top of `baseVersion` under the given generation, with
   * the binding the editor sends (workspace, page, application, engine pin,
   * session). The server answers 200 with a receipt or 409 when the lease
   * or the head moved.
   */
  save(
    api: APIRequestContext,
    a: Admission,
    o: { generation: string; baseVersion: string; enginePin: string; bytes: Buffer; editorRevision: string },
  ): Promise<APIResponse> {
    return api.post(this.url("document/save"), {
      headers: { "X-Craft-Session": a.token },
      data: {
        workspaceId: this.workspaceId,
        pageId: this.pageId,
        applicationId: this.application,
        enginePin: o.enginePin,
        sessionId: a.sessionId,
        generation: o.generation,
        baseVersion: o.baseVersion,
        editorRevision: o.editorRevision,
        contentHash: sha256(o.bytes),
        bytes: o.bytes.toString("base64"),
      },
    })
  }

  async grant(owner: APIRequestContext, userId: string, level: "read" | "comment" | "write"): Promise<void> {
    const res = await owner.put(`/api/v1/pages/${encodeURIComponent("@" + this.pageId)}/grants?${new URLSearchParams({ workspace_id: this.workspaceId })}`, {
      data: { subject_type: "user", subject: userId, level },
    })
    await json(res, 200, `grant ${level}`)
  }
}

/** Id of the person behind a session (Auth.js session endpoint). */
export async function sessionUserId(api: APIRequestContext): Promise<string> {
  const body = await json<{ user?: { id?: string } }>(await api.get("/api/auth/session"), 200, "auth session")
  if (!body.user?.id) throw new Error("auth session has no user id")
  return body.user.id
}
