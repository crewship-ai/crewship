"use client"

/**
 * Panel actions — the front half of PRD `docs/prd/pages.md` §8b.
 *
 * A page that cannot *do* anything is a report. This file is the whole of the
 * doing: the vocabulary normaliser, the one dispatcher every button goes
 * through, the host-drawn confirmation, the parameter form, and the progress
 * surface after the click. Nothing else in the tree posts an action.
 *
 * The five properties that are load-bearing, each with the paragraph of the PRD
 * that demands it:
 *
 *  1. **The button posts an action id, and the body carries only the collected
 *     inputs** (§8b.2). There is no `routine` field in `PageAction`, the
 *     normaliser does not read one off the wire, and `dispatchBody` builds
 *     `{inputs}` and nothing else. The allow-list is not a check we remember to
 *     perform — the wire format has no field a routine could travel in, so a
 *     compromised client, an injected panel or an agent cannot name one at
 *     click time. The server resolves the id against the stored spec.
 *
 *  2. **Every write goes through `useApiMutation`** (§8b.5, issue #1563).
 *     `apiFetch` resolves on 4xx/5xx; four mutations once toasted success for a
 *     write the server had refused. The hook makes the four rules structural —
 *     `res.ok` before any success, the server's own words on a refusal, nothing
 *     destroyed that a retry needs, `catch` for transport only — and hands back
 *     202 as "accepted, not completed" and 429 as `already-running` with its
 *     `Retry-After`, plus one `Idempotency-Key` per logical click reused across
 *     retries. One executor, used by every button: the convention cannot drift
 *     if there is only one place it lives.
 *
 *  3. **The confirmation is host chrome** (§8 rule 5) — shadcn `AlertDialog`,
 *     never markup a panel supplied. The panel contributes four strings and
 *     they render as text inside our dialog; it cannot fake the dialog, and it
 *     cannot skip it, because the only path from a click to `dispatch` for an
 *     action carrying `confirm` runs through `AlertDialogAction`. It is drawn
 *     ONLY when the action declares `confirm` (§8 rule 7): Anthropic's own
 *     containment write-up reports ~93 % of Claude Code prompts are approved,
 *     so a universal dialog is a rubber stamp, not a control. Friction is
 *     calibrated to blast radius by the page spec, and the spec is human-owned.
 *
 *  4. **Progress reuses what exists** (§8b.4): `useActiveRoutineRuns()` — one
 *     app-wide subscription, already mounted in `app/(dashboard)/layout.tsx` —
 *     answers "is it still running", and `<PipelineRunActivity>` draws the
 *     rail. No second progress surface, no second poll.
 *
 *  5. **A public page renders no buttons at all** (§7.3.2 rule 1). The public
 *     wire has no action field and the public grid mounts no
 *     `PanelActionsProvider`, so there is nothing for a button to be built
 *     from; `publicView` is a second, independent lock. Neither is "hidden in
 *     CSS", which the rule forbids in as many words.
 *
 * There is no `react-hook-form` here and the repo still has no forms library:
 * `SlashActionModal` (`components/features/chat/composer/slash-action-modal.tsx:185`)
 * proves a server-declared schema rendered by one field switch is sufficient,
 * and the switch below is that pattern — unknown types fall back to text so the
 * server can introduce a field type without a coordinated frontend rollout.
 */

import * as React from "react"
import Link from "next/link"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { PipelineRunActivity } from "@/components/features/activity/pipeline-run-activity"
import { useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import {
  ApiMutationError,
  useApiMutation,
  type AlreadyRunningOutcome,
  type UseApiMutationResult,
} from "@/hooks/use-api-mutation"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"

import { entityHref } from "./entity-href"
import type { EntityRef, PanelSpec } from "./types"

// ── The vocabulary (§8b.1) ─────────────────────────────────────────────────

export const ACTION_KINDS = ["call", "link", "toggle", "custom"] as const
export type ActionKind = (typeof ACTION_KINDS)[number]

export const ACTION_STYLES = ["default", "primary", "danger"] as const
export type ActionStyle = (typeof ACTION_STYLES)[number]

/** The four strings the host dialog renders. Text, never markup (§8 rule 1). */
export interface ActionConfirm {
  title: string
  body: string
  confirmLabel: string
  cancelLabel: string
}

export interface ActionInputOption {
  value: string
  label: string
}

/**
 * One field of a server-declared parameter schema. `type` is deliberately a
 * plain string, not an enum: the field switch falls back to a text input for
 * anything it does not recognise, which is what lets the server add a type
 * without shipping a frontend first (`slash-action-modal.tsx:335-347`).
 */
export interface ActionInput {
  name: string
  type: string
  label?: string
  required?: boolean
  default?: string
  placeholder?: string
  options?: ActionInputOption[]
}

/**
 * A button on a panel.
 *
 * **There is no `routine` field, and adding one would be a security
 * regression** (§8b.2). The routine a `call` action runs is named in the stored
 * page spec and resolved server-side from `id`; a client that could name one
 * would turn the allow-list back into a check somebody has to remember.
 */
export interface PageAction {
  id: string
  kind: ActionKind
  label: string
  style: ActionStyle
  confirm?: ActionConfirm
  inputs?: ActionInput[]
  /** `kind: "link"` — an internal entity, never a URL (§8 rule 3). */
  ref?: EntityRef
  /**
   * `kind: "toggle"` — the panel ids this button shows or hides
   * (`internal/pages/spec.go` `PanelAction.Target`). Local only: no request is
   * issued for a toggle, and the ids address panels on this page, not routes.
   */
  target?: string[]
}

/**
 * `narrative.v1` carries no actions in this release.
 *
 * §12's v1 line is literally *"narrative.v1, text only, no actions"*, and the
 * reason is §8 rule 9's lethal-trifecta check: the narrative panel is the one
 * an agent writes, so a panel that displays untrusted text AND dispatches is
 * exactly the combination that is staged to v1.1 behind the governed
 * `page.write` verb. The server is the first lock; this set is the second, and
 * it costs one `Set.has`.
 */
export const SCHEMAS_WITHOUT_ACTIONS: ReadonlySet<string> = new Set(["narrative.v1"])

// ── Normalising the wire ───────────────────────────────────────────────────

const ACTION_KIND_SET: ReadonlySet<string> = new Set<string>(ACTION_KINDS)
const ACTION_STYLE_SET: ReadonlySet<string> = new Set<string>(ACTION_STYLES)

function str(value: unknown): string {
  return typeof value === "string" ? value.trim() : ""
}

function toConfirm(raw: unknown): ActionConfirm | undefined {
  if (!raw || typeof raw !== "object") return undefined
  const r = raw as Record<string, unknown>
  const title = str(r.title)
  const body = str(r.body)
  // A confirm step with nothing to say is not a confirm step. Falling back to
  // our own words here would let a malformed spec produce a dialog that says
  // less than the button it is gating, which is worse than no dialog: the
  // reader learns to click through it.
  if (!title && !body) return undefined
  return {
    title: title || "Are you sure?",
    body,
    confirmLabel: str(r.confirmLabel) || str(r.confirm_label) || "Confirm",
    cancelLabel: str(r.cancelLabel) || str(r.cancel_label) || "Cancel",
  }
}

function toOptions(raw: unknown): ActionInputOption[] | undefined {
  if (!Array.isArray(raw)) return undefined
  const out: ActionInputOption[] = []
  for (const entry of raw) {
    if (typeof entry === "string") {
      const v = entry.trim()
      if (v) out.push({ value: v, label: v })
      continue
    }
    if (entry && typeof entry === "object") {
      const r = entry as Record<string, unknown>
      const value = str(r.value)
      if (value) out.push({ value, label: str(r.label) || value })
    }
  }
  return out.length > 0 ? out : undefined
}

function toInputs(raw: unknown): ActionInput[] | undefined {
  if (!Array.isArray(raw)) return undefined
  const out: ActionInput[] = []
  const seen = new Set<string>()
  for (const entry of raw) {
    if (!entry || typeof entry !== "object") continue
    const r = entry as Record<string, unknown>
    const name = str(r.name)
    if (!name || seen.has(name)) continue
    seen.add(name)
    out.push({
      name,
      type: str(r.type) || "text",
      label: str(r.label) || undefined,
      required: r.required === true,
      default: typeof r.default === "string" ? r.default : undefined,
      placeholder: str(r.placeholder) || undefined,
      options: toOptions(r.options),
    })
  }
  return out.length > 0 ? out : undefined
}

function toTarget(raw: unknown): string[] | undefined {
  if (!Array.isArray(raw)) return undefined
  const out = raw.map(str).filter((v) => v !== "")
  return out.length > 0 ? out : undefined
}

function toRef(raw: unknown): EntityRef | undefined {
  if (!raw || typeof raw !== "object") return undefined
  const r = raw as Record<string, unknown>
  const kind = str(r.kind)
  const id = str(r.id)
  if (!kind || !id) return undefined
  return { kind, id }
}

/**
 * Untrusted wire array in, `PageAction[]` out. Never throws, never invents.
 *
 * Every field is copied explicitly onto a fresh object, which is the point: a
 * `routine`, a `params`, a `url` or an `href` that a spec, an agent or a
 * compromised server put on the wire has nowhere to land and never reaches the
 * DOM or the request body. An entry missing an id or carrying a kind outside
 * the closed set is dropped whole — a button that cannot say what it does is
 * not rendered in a degraded form.
 */
export function normalizePanelActions(raw: unknown): PageAction[] {
  if (!Array.isArray(raw)) return []
  const out: PageAction[] = []
  const seen = new Set<string>()
  for (const entry of raw) {
    if (!entry || typeof entry !== "object") continue
    const r = entry as Record<string, unknown>
    const id = str(r.id)
    const kind = str(r.kind)
    if (!id || seen.has(id) || !ACTION_KIND_SET.has(kind)) continue
    seen.add(id)
    const style = str(r.style)
    out.push({
      id,
      kind: kind as ActionKind,
      // An action with no label falls back to its id rather than rendering a
      // nameless button: the reader must always be able to say what they
      // clicked afterwards.
      label: str(r.label) || id,
      style: ACTION_STYLE_SET.has(style) ? (style as ActionStyle) : "default",
      confirm: toConfirm(r.confirm),
      inputs: toInputs(r.inputs),
      ref: toRef(r.ref),
      target: toTarget(r.target),
    })
  }
  return out
}

// ── Dispatch (§8b.2, §8b.3) ────────────────────────────────────────────────

/**
 * The endpoint of §8b.2, verbatim. `workspace_id` rides on the query string
 * because §11b.1 pins Pages routes as workspace-unscoped with the workspace
 * supplied that way — the same shape `usePage` already fetches with.
 */
export function panelActionUrl(
  slug: string,
  panelId: string,
  actionId: string,
  workspaceId?: string | null,
): string {
  const path =
    `/api/v1/pages/${encodeURIComponent(slug)}` +
    `/panels/${encodeURIComponent(panelId)}` +
    `/actions/${encodeURIComponent(actionId)}`
  return workspaceId ? `${path}?workspace_id=${encodeURIComponent(workspaceId)}` : path
}

/**
 * The entire request body. One field, and a test asserts it stays that way:
 * §8b.2's guarantee is a property of the wire format, not of anyone's
 * intentions.
 */
export function dispatchBody(inputs: Record<string, string>): { inputs: Record<string, string> } {
  return { inputs }
}

/**
 * The 202 receipt (`internal/api/pages_actions.go` `dispatchReceipt`).
 *
 * It carries a `pending_id`, not a run id: §8b.3's dispatch path ENQUEUES, so
 * at the moment of the answer there is no run yet. It also names the `routine`
 * the server chose — which is not the client naming one (§8b.2 is about the
 * REQUEST) and is the only way this surface learns which routine to watch.
 */
export interface DispatchAck {
  pendingId: string | null
  /** The routine the SERVER resolved from the stored spec. */
  routine: string | null
  /** `SCHEDULED` on a fresh enqueue, `DEDUPED` on an idempotent replay. */
  status: string | null
  deduped: boolean
}

export function readDispatchAck(body: unknown): DispatchAck {
  if (!body || typeof body !== "object") {
    return { pendingId: null, routine: null, status: null, deduped: false }
  }
  const r = body as Record<string, unknown>
  return {
    pendingId: str(r.pending_id) || null,
    routine: str(r.routine) || null,
    status: str(r.status) || null,
    deduped: r.deduped === true || r.coalesced === true,
  }
}

/**
 * §8b.3's "already running" answer.
 *
 * The server's own sentence, plus the number IT sent in `Retry-After` — rule 2
 * of #1563 is that we say what the server said, and a client-side "try again
 * later" that ignored a five-second header would be us inventing the wait.
 */
export function alreadyRunningSentence(outcome: AlreadyRunningOutcome): string {
  const said = outcome.message.trim()
  // `useApiMutation`'s fallback when the body carried no message at all.
  const base = said && said !== "Already running (HTTP 429)" ? said : "Already running"
  const stem = base.replace(/[.!]+$/, "")
  const secs = outcome.retryAfterSeconds
  return secs !== null && secs > 0
    ? `${stem} — try again in ${secs} s.`
    : `${stem} — try again shortly.`
}

/**
 * Rule 2 and rule 4 of #1563 at the same time: an `ApiMutationError` carries
 * the server's own sentence and is shown verbatim; anything else out of the
 * mutation is a transport failure, which is a different fact and must not be
 * dressed up as a refusal.
 */
export function refusalSentence(error: unknown): string {
  if (error instanceof ApiMutationError) return error.message
  return "The request did not reach the server. Check your connection and try again."
}

// ── Client-registered handlers for `kind: "custom"` ────────────────────────

export interface CustomActionContext {
  action: PageAction
  panelId: string
  slug: string
  workspaceId: string | null
}

export type CustomActionHandler = (ctx: CustomActionContext) => void

/**
 * The extension point Airbnb's SDUI write-up recommends shipping from day one
 * so it does not have to be retrofitted (§8b.1). It resolves the action's own
 * id to a handler WE registered at build time — never to user-supplied code,
 * and never to anything the spec named, since `PanelAction` has no field for a
 * handler. A `Map`, not an object, so `__proto__` cannot name one.
 *
 * Empty today: no first-party custom action ships in v1, and an action whose
 * target resolves to nothing renders no button at all rather than a control
 * that silently does nothing when clicked.
 */
export const CUSTOM_ACTION_HANDLERS = new Map<string, CustomActionHandler>()

// ── The context the buttons live in ────────────────────────────────────────

interface PanelActionsContextValue {
  slug: string
  /** Overrides `useWorkspace()`; the app leaves it unset. */
  workspaceId?: string | null
  actions: ReadonlyMap<string, readonly PageAction[]>
  /** Panel ids a `toggle` has hidden. Local, per-viewer, never persisted. */
  hidden: ReadonlySet<string>
  toggleHidden: (panelIds: readonly string[]) => void
}

const PanelActionsContext = React.createContext<PanelActionsContextValue | null>(null)

export interface PanelActionsProviderProps {
  slug: string
  actions: ReadonlyMap<string, readonly PageAction[]>
  workspaceId?: string | null
  children: React.ReactNode
}

/**
 * Mounted by `page-view.tsx` around the internal grid, and by nothing else.
 *
 * The actions travel in this context rather than on `PanelSpec` for one
 * reason: absence is then the default. A surface that does not mount the
 * provider — the public page, the dashboard strip, a panel rendered in a test
 * — cannot produce a button no matter what arrives on the wire, which is how
 * §7.3.2 rule 1 stays true without anybody remembering it.
 */
export function PanelActionsProvider({
  slug,
  actions,
  workspaceId,
  children,
}: PanelActionsProviderProps) {
  // A `toggle` shows or hides panels (`PanelAction.Target`), so the state it
  // flips is the PAGE's, not the button's — it lives here, above the grid,
  // and it is view state: nothing is persisted and nothing is sent.
  const [hidden, setHidden] = React.useState<ReadonlySet<string>>(() => new Set<string>())

  const toggleHidden = React.useCallback((panelIds: readonly string[]) => {
    setHidden((prev) => {
      const next = new Set(prev)
      // All-or-nothing on the group, so a button with three targets says one
      // thing rather than three: if every target is hidden, show them all.
      const allHidden = panelIds.every((id) => prev.has(id))
      for (const id of panelIds) {
        if (allHidden) next.delete(id)
        else next.add(id)
      }
      return next
    })
  }, [])

  const value = React.useMemo<PanelActionsContextValue>(
    () => ({ slug, actions, workspaceId, hidden, toggleHidden }),
    [slug, actions, workspaceId, hidden, toggleHidden],
  )
  return <PanelActionsContext.Provider value={value}>{children}</PanelActionsContext.Provider>
}

/**
 * The panel ids a `toggle` action currently hides, for the grid to skip.
 *
 * Outside the provider it is empty, which is the honest answer everywhere a
 * toggle cannot exist in the first place.
 */
export function useHiddenPanels(): ReadonlySet<string> {
  return React.useContext(PanelActionsContext)?.hidden ?? EMPTY_HIDDEN
}

const EMPTY_HIDDEN: ReadonlySet<string> = new Set<string>()

// ── The action bar ─────────────────────────────────────────────────────────

export interface PanelActionsProps {
  panel: PanelSpec
  publicView?: boolean
}

/**
 * The gate, rendered by `PanelFrame` for every panel on every surface.
 *
 * Four independent reasons to draw nothing, checked before any hook that could
 * fetch runs: no provider (public page, dashboard strip, isolated test), a
 * public view, a sealed panel (§11b.14 — the server sent an id, a span and a
 * crew name, and a button is not among them), and a schema that carries no
 * actions in this release.
 */
export function PanelActions({ panel, publicView = false }: PanelActionsProps) {
  const ctx = React.useContext(PanelActionsContext)
  const declared = ctx?.actions.get(panel.id)
  if (!ctx || publicView || panel.sealed === true) return null
  if (!declared || declared.length === 0) return null
  if (SCHEMAS_WITHOUT_ACTIONS.has(panel.schema)) return null
  // Filtered here rather than inside the row so a panel whose only action is
  // unrenderable — a link with no resolvable ref, a custom action this build
  // has no handler for — gets no bar at all instead of an empty rule across
  // the card.
  const actions = declared.filter(isRenderable)
  if (actions.length === 0) return null
  return <PanelActionBar ctx={ctx} panelId={panel.id} actions={actions} />
}

function isRenderable(action: PageAction): boolean {
  if (action.kind === "link") return entityHref(action.ref) !== null
  if (action.kind === "custom") return CUSTOM_ACTION_HANDLERS.has(action.id)
  return true
}

interface Ack extends DispatchAck {
  actionId: string
  label: string
}

function PanelActionBar({
  ctx,
  panelId,
  actions,
}: {
  ctx: PanelActionsContextValue
  panelId: string
  actions: readonly PageAction[]
}) {
  const { workspaceId: storeWorkspaceId } = useWorkspace()
  const workspaceId = ctx.workspaceId ?? storeWorkspaceId
  // The app-wide "what is running right now" subscription (§8b.4). Reading it
  // costs one context read — the provider in app/(dashboard)/layout.tsx owns
  // the only fetch loop, and outside a provider it is an inert empty value.
  const { bySlug } = useActiveRoutineRuns()
  const [ack, setAck] = React.useState<Ack | null>(null)

  // The receipt names the routine the SERVER chose, and `bySlug` is the
  // app-wide answer to "is that routine running right now" (§8b.4). Neither
  // half is new infrastructure and neither is a poll of our own.
  const activeRun = ack?.routine ? (bySlug.get(ack.routine) ?? null) : null

  return (
    <div data-slot="panel-actions" className="flex flex-col gap-2 border-t border-border/60 pt-2">
      <div className="flex flex-wrap items-start gap-2">
        {actions.map((action) => (
          <PanelActionControl
            key={action.id}
            action={action}
            panelId={panelId}
            slug={ctx.slug}
            workspaceId={workspaceId}
            hidden={ctx.hidden}
            onToggle={ctx.toggleHidden}
            running={activeRun !== null && ack?.actionId === action.id}
            onAccepted={(next) => setAck({ ...next, actionId: action.id, label: action.label })}
          />
        ))}
      </div>
      {ack ? <ActionProgress ack={ack} workspaceId={workspaceId} run={activeRun} /> : null}
    </div>
  )
}

const STYLE_VARIANT: Record<ActionStyle, "outline" | "default" | "destructive"> = {
  default: "outline",
  primary: "default",
  danger: "destructive",
}

function PanelActionControl({
  action,
  panelId,
  slug,
  workspaceId,
  hidden,
  onToggle,
  running,
  onAccepted,
}: {
  action: PageAction
  panelId: string
  slug: string
  workspaceId: string | null
  hidden: ReadonlySet<string>
  onToggle: (panelIds: readonly string[]) => void
  running: boolean
  onAccepted: (ack: DispatchAck) => void
}) {
  const [confirmOpen, setConfirmOpen] = React.useState(false)
  const [inputsOpen, setInputsOpen] = React.useState(false)
  const [values, setValues] = React.useState<Record<string, string>>(() => seedValues(action.inputs))

  // A toggle never hides the panel its own button is on: the button would go
  // with it and there would be no way back short of a reload.
  const targets = (action.target ?? []).filter((id) => id !== panelId)
  const toggledOn = targets.length > 0 && targets.every((id) => hidden.has(id))

  const dispatch = useApiMutation<Record<string, string>, unknown>({
    request: (inputs) => ({
      input: panelActionUrl(slug, panelId, action.id, workspaceId),
      init: {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        // §8b.2: the collected inputs, and nothing else. No routine, no
        // params, no panel spec echoed back — the server already has all of
        // it, and anything we sent would be a second, weaker source of truth.
        body: JSON.stringify(dispatchBody(inputs)),
      },
    }),
    // Nothing is invalidated here on purpose. A 202 means the server accepted
    // the click, not that a panel changed; when the producer does push,
    // `page.panel.updated` invalidates the page through the subscription
    // `hooks/use-pages.ts` already holds. Invalidating now would refetch the
    // same bytes and imply the work was done.
    onOk: () => {
      toast.success(`${action.label} — done`)
      setInputsOpen(false)
    },
    onAccepted: (data) => {
      onAccepted(readDispatchAck(data))
      toast.success(`${action.label} — started`)
      setInputsOpen(false)
    },
    onAlreadyRunning: (outcome) => {
      // Not an error: the server refused to start a SECOND run, which is the
      // answer the concurrency key exists to give. The dialog stays open and
      // the values stay put, because the retry needs them.
      toast.warning(alreadyRunningSentence(outcome))
    },
    onError: (error) => {
      toast.error(refusalSentence(error))
    },
  })

  // `kind: "link"` — a route this renderer built from an entity ref. It never
  // receives a URL, so there is nothing to sanitise and nothing to trust.
  if (action.kind === "link") {
    const href = entityHref(action.ref)
    if (!href) return null
    return (
      <Button asChild variant={STYLE_VARIANT[action.style]} size="sm">
        <Link data-slot="panel-action" data-action-id={action.id} data-action-kind="link" href={href}>
          {action.label}
        </Link>
      </Button>
    )
  }

  if (action.kind === "custom" && !CUSTOM_ACTION_HANDLERS.has(action.id)) return null

  const perform = () => {
    switch (action.kind) {
      case "toggle":
        // Local only (§8b.1): it shows or hides panels on this page. No
        // request is issued, and none can be — this branch never reaches
        // `dispatch`.
        onToggle(targets)
        return
      case "custom":
        CUSTOM_ACTION_HANDLERS.get(action.id)?.({ action, panelId, slug, workspaceId })
        return
      default:
        dispatch.mutate(values)
    }
  }

  const commit = () => {
    if (action.kind === "call" && action.inputs && action.inputs.length > 0) {
      setInputsOpen(true)
      return
    }
    perform()
  }

  const onClick = () => {
    // The ONLY path from a click to `perform()` for an action that declares
    // `confirm` runs through the AlertDialog below (§8 rule 5).
    if (action.confirm) {
      setConfirmOpen(true)
      return
    }
    commit()
  }

  const busy = dispatch.isPending || running

  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Button
        type="button"
        size="sm"
        variant={STYLE_VARIANT[action.style]}
        data-slot="panel-action"
        data-action-id={action.id}
        data-action-kind={action.kind}
        data-action-style={action.style}
        aria-pressed={action.kind === "toggle" ? toggledOn : undefined}
        disabled={busy}
        onClick={onClick}
      >
        {running ? "Running…" : dispatch.isPending ? "Working…" : action.label}
      </Button>

      {/* Inline status for an action with no form. The dialog carries its own
          copy so a refusal is read where the inputs are. */}
      {!inputsOpen ? <ActionStatus dispatch={dispatch} /> : null}

      {action.confirm ? (
        <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
          <AlertDialogContent data-slot="panel-action-confirm" data-action-id={action.id}>
            <AlertDialogHeader>
              {/* Text nodes, both of them. The panel supplies strings; the
                  host supplies the dialog (§8 rule 1, §8 rule 5). */}
              <AlertDialogTitle className="text-sm">{action.confirm.title}</AlertDialogTitle>
              {action.confirm.body ? (
                <AlertDialogDescription className="text-xs">
                  {action.confirm.body}
                </AlertDialogDescription>
              ) : null}
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel className="h-7 text-xs">
                {action.confirm.cancelLabel}
              </AlertDialogCancel>
              <AlertDialogAction
                className={cn(
                  "h-7 text-xs",
                  action.style === "danger" &&
                    "bg-destructive text-destructive-foreground hover:bg-destructive/90",
                )}
                onClick={commit}
              >
                {action.confirm.confirmLabel}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      ) : null}

      {action.inputs && action.inputs.length > 0 ? (
        <ActionInputsDialog
          action={action}
          open={inputsOpen}
          onOpenChange={setInputsOpen}
          values={values}
          onChange={(name, value) => setValues((prev) => ({ ...prev, [name]: value }))}
          dispatch={dispatch}
        />
      ) : null}
    </div>
  )
}

function seedValues(inputs?: ActionInput[]): Record<string, string> {
  const seed: Record<string, string> = {}
  for (const f of inputs ?? []) seed[f.name] = f.default ?? ""
  return seed
}

// ── What the click produced ────────────────────────────────────────────────

/**
 * The one place an outcome becomes a sentence.
 *
 * A 202 says "started", never "done" — §8b.3's whole point is that the run has
 * not happened yet. A 429 says "already running" and repeats the server's
 * `Retry-After` rather than inventing a number or a generic failure. A refusal
 * says what the server said, and offers the retry that reuses the same
 * idempotency key.
 */
function ActionStatus({
  dispatch,
  showRetry = true,
}: {
  dispatch: UseApiMutationResult<Record<string, string>, unknown>
  /** Off inside the inputs dialog: the submit button there is the retry, and
   *  it mints a fresh idempotency key over the CURRENT values. Offering the
   *  same-key retry beside an editable form is how a user edits a field and
   *  gets the server's "same key, different inputs" 409 for their trouble. */
  showRetry?: boolean
}) {
  if (dispatch.isPending) return null

  if (dispatch.isAlreadyRunning && dispatch.data?.kind === "already-running") {
    return (
      <p
        role="status"
        data-slot="panel-action-status"
        data-state="already-running"
        className="type-page-meta text-muted-foreground"
      >
        {alreadyRunningSentence(dispatch.data)}
      </p>
    )
  }

  if (dispatch.error !== undefined) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <p
          role="alert"
          data-slot="panel-action-status"
          data-state="refused"
          className="type-page-meta text-destructive"
        >
          {refusalSentence(dispatch.error)}
        </p>
        {showRetry ? (
          <button
            type="button"
            data-slot="panel-action-retry"
            onClick={dispatch.retry}
            className="type-page-meta font-medium text-primary underline-offset-2 hover:underline"
          >
            Try again
          </button>
        ) : null}
      </div>
    )
  }

  if (dispatch.data?.kind === "accepted") {
    return (
      <p
        role="status"
        data-slot="panel-action-status"
        data-state="accepted"
        className="type-page-meta text-muted-foreground"
      >
        Started — the run has not finished yet.
      </p>
    )
  }

  return null
}

/**
 * The rail after the click (§8b.4).
 *
 * The click did not start a run — it enqueued one (§8b.3), so for the first
 * seconds there is a `pending_id` and nothing to draw a timeline of. The rail
 * appears when the routine the receipt named turns up in the active-run feed,
 * and until then there is one line saying exactly that.
 *
 * Rendering `PipelineRunActivity` before then would show the routine's PREVIOUS
 * run — the one failure mode worse than no progress surface, because it looks
 * like progress.
 */
function ActionProgress({
  ack,
  workspaceId,
  run,
}: {
  ack: Ack
  workspaceId: string | null
  run: { id: string; pipeline_slug: string } | null
}) {
  return (
    <div data-slot="panel-action-progress" data-action-id={ack.actionId} className="min-w-0">
      {run && workspaceId ? (
        <PipelineRunActivity
          workspaceId={workspaceId}
          slug={run.pipeline_slug}
          runId={run.id}
          title={`${ack.label} — run activity`}
        />
      ) : (
        <p className="type-page-meta text-muted-foreground">
          {ack.deduped
            ? `${ack.label} — already queued; this click joined the run that was pending.`
            : `${ack.label} — queued. It has not started yet.`}
        </p>
      )}
    </div>
  )
}

// ── Parameter collection (§8b.4, the SlashActionModal pattern) ─────────────

function ActionInputsDialog({
  action,
  open,
  onOpenChange,
  values,
  onChange,
  dispatch,
}: {
  action: PageAction
  open: boolean
  onOpenChange: (open: boolean) => void
  values: Record<string, string>
  onChange: (name: string, value: string) => void
  dispatch: UseApiMutationResult<Record<string, string>, unknown>
}) {
  const fields = action.inputs ?? []
  const [missing, setMissing] = React.useState<string[]>([])

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    // The only client-side validation, same as the slash modal: required
    // fields. Everything else is the server's to judge, and its sentence is
    // what gets shown.
    const empty = fields.filter((f) => f.required && !values[f.name]?.trim()).map((f) => f.name)
    setMissing(empty)
    if (empty.length > 0) return
    dispatch.mutate(values)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg" data-slot="panel-action-inputs" data-action-id={action.id}>
        <DialogHeader>
          <DialogTitle>{action.label}</DialogTitle>
          <DialogDescription className="text-xs">
            These values are sent with the action. Nothing else is.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4">
          {fields.map((field) => (
            <ActionField
              key={field.name}
              field={field}
              value={values[field.name] ?? ""}
              invalid={missing.includes(field.name)}
              onChange={(v) => onChange(field.name, v)}
            />
          ))}

          {/* A refusal lands HERE, beside the values that caused it, and the
              dialog stays open: #1563 rule 3 — never destroy the state a retry
              needs. Nothing below clears `values`. */}
          <ActionStatus dispatch={dispatch} showRetry={false} />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={dispatch.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={dispatch.isPending}>
              {dispatch.isPending ? "Working…" : action.label}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The field switch (`slash-action-modal.tsx:185`), narrowed to what a page
 * action needs. One switch over a server-declared type, and an unrecognised
 * type renders a text input — a dashboard older than its server collects the
 * value and lets the server validate it, rather than rendering nothing.
 */
function ActionField({
  field,
  value,
  invalid,
  onChange,
}: {
  field: ActionInput
  value: string
  invalid: boolean
  onChange: (value: string) => void
}) {
  const id = `action-field-${field.name}`
  const label = (
    <Label htmlFor={id} className="capitalize">
      {field.label || field.name.replace(/_/g, " ")}
      {field.required ? <span className="ml-1 text-destructive">*</span> : null}
    </Label>
  )
  const hint = invalid ? (
    <p role="alert" className="type-page-meta text-destructive">
      {field.label || field.name} is required.
    </p>
  ) : null

  switch (field.type) {
    case "textarea":
      return (
        <div className="space-y-1">
          {label}
          <Textarea
            id={id}
            rows={4}
            value={value}
            placeholder={field.placeholder}
            onChange={(e) => onChange(e.target.value)}
          />
          {hint}
        </div>
      )

    case "select":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value} onValueChange={onChange}>
            <SelectTrigger id={id}>
              <SelectValue placeholder={field.placeholder ?? "Choose…"} />
            </SelectTrigger>
            <SelectContent>
              {(field.options ?? []).map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {hint}
        </div>
      )

    case "number":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={id}
            type="number"
            value={value}
            placeholder={field.placeholder}
            onChange={(e) => onChange(e.target.value)}
          />
          {hint}
        </div>
      )

    case "secret":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={id}
            type="password"
            autoComplete="off"
            value={value}
            placeholder={field.placeholder}
            onChange={(e) => onChange(e.target.value)}
          />
          {hint}
        </div>
      )

    case "text":
    default:
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={id}
            value={value}
            placeholder={field.placeholder}
            onChange={(e) => onChange(e.target.value)}
          />
          {hint}
        </div>
      )
  }
}
