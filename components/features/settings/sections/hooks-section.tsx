"use client"

/**
 * Settings → Workspace → Lifecycle hooks (#2162).
 *
 * A hook is code this instance runs on its own events. A `shell` handler runs
 * `sh -c` on the crewshipd host (internal/hooks/shell.go) — 30s timeout, a
 * curated environment, no container around it. Registering one is a
 * host-execution grant, which is why validateHandlerKind demands OWNER rather
 * than the roleManage the route declares.
 *
 * Until this screen existed, `crewship hooks list` in a terminal was the only
 * way to learn that any of that was happening. That is the whole argument for
 * the section: the registry is readable by every workspace member on the
 * server (`GET /api/v1/hooks` is `authed(wsCtx(...))`), and was readable by
 * nobody in the browser.
 *
 * ── Why the shell command is printed in full ─────────────────────────────
 *
 * The list endpoint returns `handler_config` verbatim to every authenticated
 * member of the workspace, on purpose: hookRow's comment says it is rendered
 * as-is "so the caller can audit shell commands, HTTP URLs, and matcher
 * predicates without a second round trip". Masking it here would hide it from
 * the person reading the screen and from nobody else — the value is already on
 * the wire, and `crewship hooks list` prints it. If a command may carry a
 * secret, that is a server-side decision (raise the read tier and redact, the
 * way app_settings already does for `smtp.password`), and this component
 * should follow the API rather than pretend in front of it.
 *
 * ── What this section does NOT do ────────────────────────────────────────
 *
 * No registration, no editing, no bulk pause. The first two need the OWNER
 * gate for shell handlers rendered honestly; the third has no endpoint at all
 * — the API has per-hook enable and disable and nothing else, and a "pause
 * everything" button that silently acts one hook at a time is worse than no
 * button.
 */

import * as React from "react"
import { AlertTriangle, Ban, CircleSlash, Globe, Terminal, Bot } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import { Spinner } from "@/components/ui/spinner"
import { SettingsCard, SettingsEmpty } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"
import { cn } from "@/lib/utils"

/**
 * Events that fire at a call site which can still cancel the operation.
 *
 * Mirrors `Event.SupportsBlocking()` in internal/hooks/types.go. Read off the
 * EVENT, never off the row's `blocking` column: a post- or on-event row can
 * carry `blocking: true` in the database — nothing rejects it at write time —
 * and rendering that as "blocking" would promise a veto the dispatcher never
 * asks for. A Block outcome on a post-event arrives too late to undo anything.
 */
const BLOCKING_EVENTS = new Set([
  "pre_task_delegation",
  "pre_agent_start",
  "pre_llm_call",
  "pre_memory_write",
  "pre_peer_conversation",
])

/**
 * Events that exist as constants but are refused for new registrations.
 *
 * `pre_tool_call` is deliberately absent from `hooks.AllEvents`, so
 * `ValidateEvent` rejects it: Crewship parses the stream a driven CLI emits,
 * and by the time a tool_call reaches the orchestrator the tool has already
 * run. Rows predating that decision still list, still toggle, and can never
 * fire — which is exactly the silent failure this screen exists to end.
 */
const RETIRED_EVENTS = new Set(["pre_tool_call"])

const HANDLER_ICON: Record<string, React.ElementType> = {
  shell: Terminal,
  http: Globe,
  subagent: Bot,
}

export interface HookRow {
  id: string
  workspace_id: string
  crew_id?: string
  event: string
  handler_kind: string
  handler_config?: Record<string, unknown>
  enabled: boolean
  blocking: boolean
  created_by?: string
  created_at: string
  updated_at: string
}

interface JournalEntry {
  id: string
  ts: string
  entry_type: string
  actor_id?: string
  payload?: Record<string, unknown>
}

/** The newest hook.fired/hook.blocked outcome per hook id. */
interface LastResult {
  outcome: string
  ts: string
  latencyMs?: number
  message?: string
}

export interface HooksSectionProps {
  workspaceId: string
  /** The caller's workspace role. */
  role: string | null | undefined
}

/**
 * `handler_config.command` for shell, `.url` for http, `.agent_id` for
 * subagent — the three keys internal/hooks validates on write.
 */
function targetOf(hook: HookRow): string {
  const cfg = hook.handler_config ?? {}
  const key = hook.handler_kind === "http" ? "url" : hook.handler_kind === "subagent" ? "agent_id" : "command"
  const value = cfg[key]
  return typeof value === "string" && value !== "" ? value : "—"
}

/** Newest-first per hook, so a hook that failed after passing reads as failed. */
function lastResults(entries: JournalEntry[]): Record<string, LastResult> {
  const out: Record<string, LastResult> = {}
  for (const e of entries) {
    const payload = e.payload ?? {}
    const hookID = typeof payload.hook_id === "string" ? payload.hook_id : e.actor_id
    if (!hookID) continue
    const outcome = typeof payload.outcome === "string" ? payload.outcome : "fired"
    const prev = out[hookID]
    // The API orders newest first, but a client that sorts is a client that
    // survives the day someone adds a cursor or changes the default order.
    if (prev && Date.parse(prev.ts) >= Date.parse(e.ts)) continue
    out[hookID] = {
      outcome,
      ts: e.ts,
      latencyMs: typeof payload.latency_ms === "number" ? payload.latency_ms : undefined,
      message: typeof payload.message === "string" ? payload.message : undefined,
    }
  }
  return out
}

function relativeTime(iso: string): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ""
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.round(secs / 60)}m ago`
  if (secs < 86400) return `${Math.round(secs / 3600)}h ago`
  return `${Math.round(secs / 86400)}d ago`
}

function OutcomeCell({ result, windowCapped }: { result?: LastResult; windowCapped: boolean }) {
  // "Never fired" is a claim about all of history, and the journal read is one
  // workspace-wide page of hook entries. When the server says there is more
  // (next_cursor), an older result for this hook may sit past the page, so the
  // honest line is that we have not seen one recently — the same rule the
  // registry read follows for "no hooks" versus "could not ask".
  if (!result) {
    return (
      <span className="text-muted-foreground/70">
        {windowCapped ? "No recent activity" : "Never fired"}
      </span>
    )
  }
  const tone =
    result.outcome === "pass"
      ? "text-success"
      : result.outcome === "block"
        ? "text-warn"
        : "text-destructive"
  return (
    <span className="inline-flex items-baseline gap-1.5">
      <span className={cn("font-medium", tone)}>{result.outcome}</span>
      <span className="text-muted-foreground/70">{relativeTime(result.ts)}</span>
    </span>
  )
}

export function HooksSection({ workspaceId, role }: HooksSectionProps) {
  const mayToggle = isAdminTier(role)

  const [hooks, setHooks] = React.useState<HookRow[] | null>(null)
  const [results, setResults] = React.useState<Record<string, LastResult>>({})
  /** The journal had more matching entries than the page we asked for. */
  const [windowCapped, setWindowCapped] = React.useState(false)
  const [readError, setReadError] = React.useState<string | null>(null)
  const [toggleError, setToggleError] = React.useState<string | null>(null)
  // One id per in-flight request. A single id re-enabled every switch as soon
  // as ANY toggle finished, so finishing hook A unlocked hook B mid-flight.
  const [pending, setPending] = React.useState<ReadonlySet<string>>(() => new Set())

  React.useEffect(() => {
    let cancelled = false
    setHooks(null)
    setReadError(null)

    apiFetch(`/api/v1/hooks?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then(async (res) => {
        if (!res.ok) throw new Error(String(res.status))
        return (await res.json()) as { rows?: HookRow[] }
      })
      .then((body) => {
        if (!cancelled) setHooks(Array.isArray(body?.rows) ? body.rows : [])
      })
      .catch(() => {
        // "No hooks are registered" and "we could not ask" are different
        // statements, and only one of them is safe to make about code that
        // runs on the host.
        if (!cancelled) setReadError("Couldn't read the hook registry for this workspace.")
      })

    // The outcome column is a bonus on top of the registry: if the journal is
    // unreachable the rows still carry event, handler and target, so this
    // failure is swallowed rather than blanking the section.
    apiFetch(
      `/api/v1/journal?workspace_id=${encodeURIComponent(workspaceId)}` +
        `&entry_type=hook.fired,hook.blocked&limit=200`,
    )
      .then(async (res) => {
        if (!res.ok) throw new Error(String(res.status))
        return (await res.json()) as { entries?: JournalEntry[]; next_cursor?: string }
      })
      .then((body) => {
        if (cancelled) return
        setResults(lastResults(Array.isArray(body?.entries) ? body.entries : []))
        setWindowCapped(Boolean(body?.next_cursor))
      })
      .catch(() => {
        if (cancelled) return
        setResults({})
        // We asked and failed, so we know nothing about any hook's history —
        // which is the capped case, not the "never fired" one.
        setWindowCapped(true)
      })

    return () => {
      cancelled = true
    }
  }, [workspaceId])

  const toggle = React.useCallback(
    async (hook: HookRow) => {
      const next = !hook.enabled
      setPending((prev) => new Set(prev).add(hook.id))
      setToggleError(null)
      // Optimistic, then reverted on refusal — the switch is the one control
      // here and a control that lags a round trip reads as broken.
      setHooks((prev) => prev?.map((h) => (h.id === hook.id ? { ...h, enabled: next } : h)) ?? prev)
      try {
        const res = await apiFetch(
          `/api/v1/hooks/${encodeURIComponent(hook.id)}/${next ? "enable" : "disable"}` +
            `?workspace_id=${encodeURIComponent(workspaceId)}`,
          { method: "POST" },
        )
        if (!res.ok) throw new Error(String(res.status))
      } catch {
        setHooks((prev) => prev?.map((h) => (h.id === hook.id ? { ...h, enabled: hook.enabled } : h)) ?? prev)
        setToggleError(
          next
            ? "Couldn't change the hook — enabling it was refused."
            : "Couldn't change the hook — disabling it was refused.",
        )
      } finally {
        setPending((prev) => {
          const next = new Set(prev)
          next.delete(hook.id)
          return next
        })
      }
    },
    [workspaceId],
  )

  const retiredCount = (hooks ?? []).filter((h) => RETIRED_EVENTS.has(h.event)).length

  return (
    <div className="space-y-5">
      <SettingsCard
        title="Lifecycle hooks"
        description="Code this workspace runs on platform events. Shell handlers run on the crewshipd host."
      >
        {readError ? (
          <SettingsEmpty>
            <span className="inline-flex items-center gap-1.5 text-destructive">
              <AlertTriangle className="h-3.5 w-3.5" />
              {readError}
            </span>
          </SettingsEmpty>
        ) : hooks === null ? (
          <SettingsEmpty>
            <Spinner className="mx-auto h-4 w-4" />
          </SettingsEmpty>
        ) : hooks.length === 0 ? (
          <SettingsEmpty>
            No hooks are registered in this workspace. Register one with{" "}
            <code className="font-mono">crewship hooks create</code>.
          </SettingsEmpty>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-border/40 text-[10px] uppercase tracking-wider text-muted-foreground">
                  <th className="px-4 py-2 text-left font-medium">Hook</th>
                  <th className="px-4 py-2 text-left font-medium">Handler</th>
                  <th className="px-4 py-2 text-left font-medium">Last result</th>
                  {mayToggle && <th className="px-4 py-2 text-right font-medium">Enabled</th>}
                </tr>
              </thead>
              <tbody>
                {hooks.map((hook) => {
                  const retired = RETIRED_EVENTS.has(hook.event)
                  const gateable = BLOCKING_EVENTS.has(hook.event)
                  const HandlerIcon = HANDLER_ICON[hook.handler_kind] ?? Terminal
                  const target = targetOf(hook)
                  return (
                    <tr
                      key={hook.id}
                      className={cn(
                        "border-b border-border/25 last:border-b-0",
                        retired && "opacity-60",
                      )}
                    >
                      {/* Identity: what fires it, over which crews, and whether
                          it can veto. Three facts about the same thing, so they
                          share a cell rather than costing three columns the
                          settings pane has no width for. */}
                      <td className="px-4 py-2.5 align-top">
                        <div className="flex items-center gap-1.5">
                          <span className="font-mono text-foreground">{hook.event}</span>
                          {retired && (
                            <Badge variant="outline" className="h-4 px-1 text-[9px] uppercase">
                              Retired
                            </Badge>
                          )}
                        </div>
                        <div className="mt-0.5 flex items-center gap-1.5 text-[11px] text-muted-foreground/80">
                          <span>{hook.crew_id ? hook.crew_id : "all crews"}</span>
                          <span aria-hidden>·</span>
                          {gateable && hook.blocking ? (
                            <span className="inline-flex items-center gap-1 text-warn">
                              <Ban className="h-3 w-3" />
                              Blocking
                            </span>
                          ) : gateable ? (
                            <span>Observes</span>
                          ) : (
                            <span title="This event fires after the fact — a block would arrive too late to undo anything.">
                              n/a
                            </span>
                          )}
                        </div>
                      </td>
                      <td className="px-4 py-2.5 align-top">
                        <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                          <HandlerIcon className="h-3 w-3" />
                          {hook.handler_kind}
                        </span>
                        {/* Wrapped, not truncated. The whole argument for
                            printing the command is that someone can audit it,
                            and an ellipsis with a hover title is neither
                            readable on a touch device nor selectable. */}
                        <span className="mt-0.5 block max-w-[22rem] break-all font-mono text-[11px] text-muted-foreground/80">
                          {target}
                        </span>
                      </td>
                      <td className="px-4 py-2.5 align-top whitespace-nowrap">
                        <OutcomeCell result={results[hook.id]} windowCapped={windowCapped} />
                      </td>
                      {mayToggle && (
                        <td className="px-4 py-2.5 text-right align-top">
                          <Switch
                            checked={hook.enabled}
                            disabled={pending.has(hook.id)}
                            onCheckedChange={() => toggle(hook)}
                            aria-label={`${hook.enabled ? "Disable" : "Enable"} ${hook.event} hook`}
                          />
                        </td>
                      )}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </SettingsCard>

      {toggleError && (
        <p className="flex items-center gap-1.5 text-[11px] text-destructive">
          <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
          {toggleError}
        </p>
      )}

      {retiredCount > 0 && (
        <p className="flex items-start gap-1.5 text-[11px] text-muted-foreground">
          <CircleSlash className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            A retired event never fires. <code className="font-mono">pre_tool_call</code> was withdrawn
            because Crewship reads a driven CLI&apos;s stream, and a tool call has already run by the time
            the orchestrator sees it. The rows remain registerable-looking but dead; delete them with{" "}
            <code className="font-mono">crewship hooks delete</code>.
          </span>
        </p>
      )}

      {!mayToggle && hooks !== null && hooks.length > 0 && (
        <p className="text-[11px] text-muted-foreground">
          Only owners and admins can enable or disable a hook. Creating a shell handler needs owner,
          because it runs a command on the host this instance is installed on.
        </p>
      )}
    </div>
  )
}
