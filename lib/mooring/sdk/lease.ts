/** Mooring SDK · lease. A Craft document has at most one writer: the server's
 * lease names its holder, a generation and an expiry. This module reads the
 * server's view and asks for transitions; the Go backend decides each one and
 * fences every write by generation. Nothing here grants authority. */
import { validNativeVersion } from "../native-draft"
import type { NativeDocumentClient } from "../native-document-client"
import { NativeSaveError } from "../single-editor-save"

export interface LeaseActor { actor_id: string; display_name: string; kind: "human" | "agent" }
export interface LeaseHandoffRequest extends LeaseActor { request_id: string }
/** The server's lease view, exactly as `GET …/craft/lease` and the `lease`
 * event deliver it. It never carries the private admission bearer. */
export interface LeaseView {
  holder: LeaseActor | null
  can_edit?: boolean
  can_stop_agent?: boolean
  mine: boolean
  generation: string | null
  can_write: boolean
  acquired_at: string | null
  expires_at: string | null
  handoff_requested_by: LeaseHandoffRequest[]
}
export type LeaseAction = "acquire" | "release" | "request-handoff" | "handoff"
/** The authorized transport: the host's admitted Craft client. */
export type LeaseTransport = Pick<NativeDocumentClient, "lease" | "leaseAction">
export type LeaseFailure = "invalid_lease" | "stale_generation" | "unknown_request" | "final_save_unconfirmed" | "draft_capture_failed" | "custody_transfer_unconfirmed" | "conflict" | "write_revoked"
export class LeaseError extends Error {
  constructor(readonly reason: LeaseFailure) { super(reason) }
}

const object = (v: unknown): v is Record<string, unknown> => Boolean(v) && typeof v === "object" && !Array.isArray(v)
const text = (v: unknown): v is string => typeof v === "string" && v.length > 0 && v.length <= 512 && !v.includes("\0")
const actor = (v: unknown) => object(v) && text(v.actor_id) && text(v.display_name) && (v.kind === "human" || v.kind === "agent")

/** Validates a server lease view with the same rules the native Craft host
 * applies. An invalid view is refused, never repaired. */
export function parseLease(raw: unknown): LeaseView {
  if (!object(raw) || typeof raw.mine !== "boolean" || typeof raw.can_write !== "boolean" || !(raw.generation === null || validNativeVersion(raw.generation) && raw.generation !== "0") || !(raw.acquired_at === null || text(raw.acquired_at)) || !(raw.expires_at === null || text(raw.expires_at)) || !Array.isArray(raw.handoff_requested_by) || raw.handoff_requested_by.length > 128) throw new LeaseError("invalid_lease")
  if (raw.holder !== null && !actor(raw.holder) || raw.handoff_requested_by.some(v => !actor(v) || !object(v) || !text(v.request_id))) throw new LeaseError("invalid_lease")
  if (raw.can_edit !== undefined && typeof raw.can_edit !== "boolean") throw new LeaseError("invalid_lease")
  if (raw.can_stop_agent !== undefined && typeof raw.can_stop_agent !== "boolean") throw new LeaseError("invalid_lease")
  if (raw.mine && (!raw.holder || !raw.generation)) throw new LeaseError("invalid_lease")
  return raw as unknown as LeaseView
}

/** True only while this viewer holds an unexpired, writable lease, optionally
 * at an exact generation. This is advisory: the server re-checks every write. */
export function ownsLease(view: LeaseView, generation?: string, now = Date.now()): boolean {
  return view.can_edit === true && view.mine && view.can_write && view.generation !== null && (!generation || view.generation === generation) && typeof view.expires_at === "string" && Date.parse(view.expires_at) > now
}

export interface LeaseControls {
  /** "Upravuje X" or "Volné", the label every Craft app shows. */
  label: string
  holderName: string | null
  holderKind: "human" | "agent" | null
  generation: string | undefined
  readOnly: boolean
  canAcquire: boolean
  canRequestHandoff: boolean
  canStopAgent: boolean
  canRelease: boolean
  handoffTargets: { request_id: string; label: string }[]
  /** The viewer held the lease, lost write, and keeps a local draft. */
  draftKeptReadOnly: boolean
}
/** The controls the native lease bar renders for a view; a Craft app renders
 * the same affordances from this description instead of re-deriving them. */
export function describeLease(view: LeaseView, mayEdit: boolean): LeaseControls {
  const generation = validNativeVersion(view.generation) && view.generation !== "0" ? view.generation : undefined
  const mine = mayEdit && view.mine && view.can_write && generation !== undefined
  const name = view.holder ? view.holder.display_name.trim() || (view.holder.kind === "agent" ? "Agent" : "Někdo") : null
  return {
    label: name ? `Upravuje ${name}` : "Volné",
    holderName: name,
    holderKind: view.holder?.kind ?? null,
    generation,
    readOnly: !mayEdit,
    canAcquire: mayEdit && !view.holder,
    canRequestHandoff: mayEdit && !!view.holder && !view.mine,
    canStopAgent: mayEdit && view.holder?.kind === "agent" && view.can_stop_agent === true,
    canRelease: mine,
    handoffTargets: mine ? view.handoff_requested_by.map(r => ({ request_id: r.request_id, label: `Předat uživateli ${r.display_name.trim() || (r.kind === "agent" ? "Agent" : "Uživatel")}` })) : [],
    draftKeptReadOnly: mayEdit && view.mine && !view.can_write,
  }
}

/** Orders lease observations from a GET and the event stream. A view at an
 * older generation than one already seen is a late delivery and is ignored,
 * so the displayed holder never rolls back. */
export function createLeaseState() {
  let current: LeaseView | undefined, highest: bigint | undefined
  return {
    get current() { return current },
    observe(raw: unknown): LeaseView | undefined {
      const view = parseLease(raw)
      if (view.generation !== null) {
        const generation = BigInt(view.generation)
        if (highest !== undefined && generation < highest) return undefined
        highest = generation
      }
      current = view
      return view
    },
  }
}

/** What a transfer needs from the holder's save lifecycle: the
 * `createSingleEditorSave` controller and the generation it was opened at. */
export interface LeaseCustody {
  generation: string
  flush(): Promise<void>
  confirmedVersion(): string | undefined
  revokeWrite(): Promise<boolean>
}

function mapped(error: unknown): never {
  if (error instanceof NativeSaveError && (error.reason === "conflict" || error.reason === "write_revoked")) throw new LeaseError(error.reason)
  throw error
}

/** Lease transitions over the authorized transport. Release and hand-off run
 * the host's custody order: confirm the final save, revoke local writing and
 * capture a durable draft, then ask the server, then prove the old generation
 * is gone. A failed step stops before the next one; the draft stays local. */
export function createLeaseActions(transport: LeaseTransport, signal: AbortSignal) {
  const proof = async () => parseLease(await transport.lease(signal))
  async function simple(action: "acquire" | "request-handoff") {
    try { await transport.leaseAction(action, {}, signal) } catch (error) { mapped(error) }
    return proof()
  }
  async function transfer(action: "release" | "handoff", view: LeaseView, custody: LeaseCustody, requestId?: string) {
    if (!ownsLease(view, custody.generation)) throw new LeaseError("stale_generation")
    if (action === "handoff" && !view.handoff_requested_by.some(r => r.request_id === requestId)) throw new LeaseError("unknown_request")
    try { await custody.flush() } catch { throw new LeaseError("final_save_unconfirmed") }
    if (custody.confirmedVersion() === undefined) throw new LeaseError("final_save_unconfirmed")
    if (!await custody.revokeWrite()) throw new LeaseError("draft_capture_failed")
    const body = action === "handoff" ? { generation: custody.generation, request_id: requestId! } : { generation: custody.generation }
    try { await transport.leaseAction(action, body, signal) } catch (error) { mapped(error) }
    const after = await proof()
    if (after.mine && after.generation === custody.generation) throw new LeaseError("custody_transfer_unconfirmed")
    return after
  }
  return {
    read: proof,
    acquire: () => simple("acquire"),
    requestHandoff: () => simple("request-handoff"),
    release: (view: LeaseView, custody: LeaseCustody) => transfer("release", view, custody),
    handOff: (view: LeaseView, requestId: string, custody: LeaseCustody) => transfer("handoff", view, custody, requestId),
  }
}
