import React from "react"
import { toast } from "sonner"
import { RefreshCw, AlertTriangle, ExternalLink, Container, Plug, Bot, ScrollText, Wrench, Trash2, Loader2 } from "lucide-react"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { RuntimeIcon, runtimeBrand } from "@/components/icons/runtime-icons"
import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import { cn } from "@/lib/utils"
import type { AgentsStatus, DaemonStatus } from "../types"
import { Kpi } from "./admin-kit"
import { formatUptime, goDurationSeconds } from "./overview-tab"

/** One crew hardening control the runtime in use is measured not to deliver. */
export interface RuntimeGap {
  /** The control that is dropped, e.g. "GroupAdd". */
  control: string
  /** What breaks because of it, in an operator's terms. */
  detail: string
}

/** One container runtime present on the host, as GET /api/v1/system/runtime reports it. */
export interface RuntimeEntry {
  runtime: string
  version: string
  socket: string
  /**
   * True for the single runtime this server actually connected to. No entry
   * carries it when the server has no container provider at all — a runtime
   * being installed and a runtime being used are different facts.
   */
  in_use: boolean
  /**
   * Controls this runtime will not honour (#1672). The server sends them on the
   * `in_use` entry only and omits the key entirely when there are none, so this
   * is optional twice over — an older server does not send it at all.
   */
  gaps?: RuntimeGap[]
}

interface RuntimeTabProps {
  runtimeChecking: boolean
  runtimeAvailable: boolean | null
  allRuntimes: RuntimeEntry[]
  runtimeInstallLinks: Record<string, string>
  onCheckRuntime: () => void
  /** Scope for the reads and maintenance actions (the admin API is
   *  workspace-scoped by middleware). */
  workspaceId: string | null
}

// This panel deliberately does NOT poll. Every request behind it re-probes
// every candidate socket — there is no cache — and while that is cheap
// (single-digit milliseconds, concurrent, bounded by the request context) it is
// a cost per open admin tab for information that changes when someone installs
// a runtime, i.e. never on its own. It loads with the tab and refreshes when
// the operator asks. See the note on SystemHandler.inventory.

const RuntimeInventory = React.memo(function RuntimeInventory({
  runtimeChecking,
  runtimeAvailable,
  allRuntimes,
  runtimeInstallLinks,
  onCheckRuntime,
}: Omit<RuntimeTabProps, "workspaceId">) {
  // The runtime in use goes first, wherever the server listed it. The server's
  // order is candidate-probe order, which is not a ranking and is not what an
  // operator opening this panel is looking for.
  const ordered = React.useMemo(
    () => [...allRuntimes].sort((a, b) => Number(b.in_use) - Number(a.in_use)),
    [allRuntimes],
  )
  const anyInUse = ordered.some((rt) => rt.in_use)
  const present = React.useMemo(
    () => new Set(allRuntimes.map((rt) => rt.runtime)),
    [allRuntimes],
  )
  const missing = Object.entries(runtimeInstallLinks).filter(([key]) => !present.has(key))

  return (
      <SettingsCard icon={Container} tint="var(--purple)"
        title="Container runtimes"
        description="What this host has installed, and the one Crewship drives."
        actions={
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={onCheckRuntime}
            disabled={runtimeChecking}
          >
            <RefreshCw className={cn("mr-1.5 h-3 w-3", runtimeChecking && "animate-spin")} />
            Re-detect
          </Button>
        }
        padded
      >
        <div className="space-y-3">
          {runtimeChecking && (
            <div className="flex items-center gap-2">
              <RefreshCw className="h-3 w-3 animate-spin text-muted-foreground" />
              <span className="text-xs text-muted-foreground">Detecting runtimes…</span>
            </div>
          )}

          {!runtimeChecking && runtimeAvailable && ordered.length > 0 && (
            <>
              <div className="flex flex-col gap-2">
                {ordered.map((rt) => {
                  const brand = runtimeBrand(rt.runtime)
                  return (
                    <div key={rt.runtime + rt.socket} className="flex flex-col gap-1">
                      <div
                        data-testid={`runtime-row-${rt.runtime}`}
                        className={cn(
                          "flex items-center gap-3 rounded-lg border px-3 py-2",
                          rt.in_use
                            ? "border-primary/30 bg-primary/[0.05] py-3"
                            : "border-border/60 bg-foreground/[0.02]",
                        )}
                      >
                        <span
                          data-testid="runtime-icon"
                          className={cn("flex shrink-0 items-center justify-center", rt.in_use ? "h-8 w-8" : "h-6 w-6")}
                        >
                          <RuntimeIcon runtime={rt.runtime} className={rt.in_use ? "h-5 w-5" : "h-4 w-4"} />
                        </span>
                        <div className="min-w-0 flex-1">
                          <div className={cn("flex items-baseline gap-2", rt.in_use ? "text-sm" : "text-xs")}>
                            <span className="font-medium">{brand.label}</span>
                            {rt.version && (
                              <span className="font-mono text-muted-foreground">{rt.version}</span>
                            )}
                          </div>
                          {rt.socket && (
                            <p className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">
                              {rt.socket}
                            </p>
                          )}
                        </div>
                        <StatusBadge
                          status={rt.in_use ? "COMPLETED" : "PENDING"}
                          label={rt.in_use ? "In use" : "Detected"}
                          className="text-[10px]"
                        />
                      </div>
                      {/*
                        Attached to the row rather than collected into one
                        notice at the foot of the card: a gap is a property of a
                        specific daemon at a specific version, and detaching it
                        from the row that names them is how it stops being
                        actionable. Only the in_use entry ever carries any.
                      */}
                      {rt.gaps && rt.gaps.length > 0 && (
                        <div
                          data-testid={`runtime-gaps-${rt.runtime}`}
                          className="rounded-lg border border-warn/40 bg-warn/5 px-3 py-2 text-[11px] text-warn"
                        >
                          {rt.gaps.map((gap) => (
                            <p key={gap.control} className="flex items-start gap-1.5">
                              <AlertTriangle className="mt-[1px] h-3 w-3 shrink-0" />
                              <span>
                                <span className="font-mono font-medium">{gap.control}</span> is not
                                honoured — {gap.detail}
                              </span>
                            </p>
                          ))}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>

              {!anyInUse && (
                <p
                  data-testid="runtime-none-in-use"
                  className="rounded-lg border border-warn/40 bg-warn/5 px-3 py-2 text-[11px] text-warn"
                >
                  No container runtime in use. These are installed, but this server started
                  without a container provider — with <code>--no-docker</code>, or because the
                  provider failed to start. Agents cannot run until it has one.
                </p>
              )}

              {/*
                The honesty requirement (#1690). `container.provider` accepts
                only docker, apple or auto — there is no value for orbstack,
                colima, rancher or podman, so nothing above is a choice the
                operator can make here. Saying "the rest are what you could
                switch to" is the mistake the CLI made (#1689) and it is not
                repeated in pixels. These two levers are the real ones.
              */}
              <p
                data-testid="runtime-switch-note"
                className="text-[11px] leading-relaxed text-muted-foreground"
              >
                Crewship drives one runtime at a time; this is a report, not a setting.
                Docker-compatible runtimes share one API and the first socket that answers
                wins — point <code className="font-mono">DOCKER_HOST</code> at the daemon you
                want. Apple Containers is its own provider, chosen with{" "}
                <code className="font-mono">container.provider</code>.
              </p>
            </>
          )}

          {!runtimeChecking && !runtimeAvailable && (
            <div className="flex items-center gap-3">
              <AlertTriangle className="h-4 w-4 shrink-0 text-warn" />
              <div className="min-w-0">
                <div className="text-xs font-medium">No runtime detected</div>
                <p className="text-[11px] text-muted-foreground">
                  Install a container runtime to enable agent containers.
                </p>
              </div>
            </div>
          )}

          {!runtimeChecking && missing.length > 0 && (
            <div className="space-y-2">
              {runtimeAvailable && ordered.length > 0 && (
                <p className="font-mono text-[10.5px] uppercase tracking-[0.08em] text-muted-foreground-soft">
                  Also supported
                </p>
              )}
              <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-3">
                {missing.map(([key, url]) => (
                  <a
                    key={key}
                    data-testid={`runtime-install-${key}`}
                    href={url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex min-w-0 items-center gap-2 rounded-md border border-border/60 bg-foreground/[0.02] px-2.5 py-1.5 text-xs transition-colors hover:border-border hover:bg-foreground/[0.04]"
                  >
                    <RuntimeIcon runtime={key} className="h-3.5 w-3.5 shrink-0" />
                    <span className="truncate font-medium">{runtimeBrand(key).label}</span>
                    <ExternalLink className="ml-auto h-2.5 w-2.5 text-muted-foreground" />
                  </a>
                ))}
              </div>
            </div>
          )}
        </div>
      </SettingsCard>
  )
})

// ── The tab ─────────────────────────────────────────────────────────────────

interface LogLevelState {
  level: string
  baseline: string
  expires_at?: string | null
}

/**
 * Admin › Runtime: what runs the agents on this host, and the levers an
 * operator actually has over it.
 *
 *   1. Figures: the runtime in use, the host daemon, agents by state, the
 *      log level.
 *   2. Container runtimes: the inventory (a report, not a setting).
 *   3. Logging: raise or lower verbosity for a while, then back to baseline.
 *   4. Maintenance: find and remove orphaned containers, legacy resources,
 *      and — separated, confirmed — every crew runtime in this workspace.
 *
 * Every figure is what the server reports; nothing here assumes a platform.
 */
export const RuntimeTab = React.memo(function RuntimeTab(props: RuntimeTabProps) {
  const { workspaceId, allRuntimes, runtimeAvailable } = props
  const inUse = allRuntimes.find((rt) => rt.in_use)
  const ws = workspaceId ? encodeURIComponent(workspaceId) : ""
  const daemon = useAdminRead<DaemonStatus>(workspaceId ? `/api/v1/crewshipd?workspace_id=${ws}` : null)
  const agents = useAdminRead<AgentsStatus>(workspaceId ? `/api/v1/agents/crews-status?workspace_id=${ws}` : null)
  const log = useAdminRead<LogLevelState>(workspaceId ? `/api/v1/admin/log-level?workspace_id=${ws}` : null)
  const daemonUp = goDurationSeconds(daemon.data?.uptime)

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4" data-slot="runtime-kpis">
        <Kpi icon={Container} tint="var(--purple)" label="Runtime"
          value={runtimeAvailable === null ? undefined : inUse ? `${runtimeBrand(inUse.runtime).label} ${inUse.version ?? ""}`.trim() : "None"}
          sub={inUse ? "Driving the agents" : runtimeAvailable ? "Detected, none in use" : "Not detected"} />
        <Kpi icon={Plug} tint="var(--primary)" label="Host daemon"
          value={daemon.loading ? undefined : daemon.data ? (daemon.data.status === "ok" ? "Healthy" : daemon.data.status) : "—"}
          sub={daemon.data ? `${daemon.data.connections} connection${daemon.data.connections === 1 ? "" : "s"}${daemonUp !== null ? ` · up ${formatUptime(daemonUp)}` : ""}` : undefined} />
        <Kpi icon={Bot} tint="var(--success)" label="Agents"
          value={agents.loading ? undefined : agents.data ? `${agents.data.running} running` : "—"}
          sub={agents.data ? `${agents.data.idle} idle · ${agents.data.queued} queued${agents.data.error ? ` · ${agents.data.error} in error` : ""}` : undefined} />
        <Kpi icon={ScrollText} tint="var(--info)" label="Log level"
          value={log.loading ? undefined : log.data?.level ?? "—"}
          sub={log.data ? (log.data.expires_at ? `until ${clock(log.data.expires_at)}` : log.data.level === log.data.baseline ? "Baseline" : `Baseline ${log.data.baseline}`) : undefined} />
      </div>

      <RuntimeInventory {...props} />

      {workspaceId && <LoggingCard workspaceId={workspaceId} state={log.data} onChange={log.set} />}
      {workspaceId && <MaintenanceCard workspaceId={workspaceId} />}
    </div>
  )
})

/** One read on mount; `set` lets a write replace the answer. */
function useAdminRead<T>(url: string | null) {
  const [data, setData] = React.useState<T | null>(null)
  const [loading, setLoading] = React.useState(!!url)
  React.useEffect(() => {
    if (!url) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await apiFetch(url)
        const body = res?.ok ? ((await res.json()) as T) : null
        if (!cancelled) setData(body)
      } catch {
        if (!cancelled) setData(null)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [url])
  return { data, loading, set: setData }
}

const clock = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })

const LEVELS = ["debug", "info", "warn", "error"] as const
const DURATIONS = [
  { label: "15 min", seconds: 900 },
  { label: "1 hour", seconds: 3600 },
  { label: "4 hours", seconds: 14400 },
] as const

/** Segmented choice as plain pressed buttons — this tab offers no select. */
function Segmented<T extends string | number>({ label, options, value, onChange, disabled }: {
  label: string; options: { value: T; label: string }[]; value: T; onChange: (v: T) => void; disabled?: boolean
}) {
  return (
    <div className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={String(o.value)} type="button" aria-pressed={value === o.value} disabled={disabled} onClick={() => onChange(o.value)}
          className={cn("h-7 rounded-md px-2.5 text-xs transition-colors disabled:opacity-50",
            value === o.value ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}>
          {o.label}
        </button>
      ))}
    </div>
  )
}

function LoggingCard({ workspaceId, state, onChange }: { workspaceId: string; state: LogLevelState | null; onChange: (s: LogLevelState) => void }) {
  const [level, setLevel] = React.useState<string>("debug")
  const [ttl, setTtl] = React.useState<number>(900)
  const [busy, setBusy] = React.useState(false)
  const put = async (body: { level: string; ttl_seconds: number }, done: string) => {
    setBusy(true)
    try {
      const res = await apiFetch(`/api/v1/admin/log-level?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      })
      if (!res.ok) { toast.error(await readApiError(res, "The log level was not changed")); return }
      onChange(await res.json())
      toast.success(done)
    } catch {
      toast.error("The log level was not changed")
    } finally {
      setBusy(false)
    }
  }
  const overridden = !!state && (state.level !== state.baseline || !!state.expires_at)
  return (
    <SettingsCard icon={ScrollText} tint="var(--info)" title="Logging"
      description="Raise verbosity while you chase something; it drops back to the baseline by itself.">
      <SettingsRow label="Current level" description={state ? `Baseline ${state.baseline}` : undefined}>
        {state ? (
          <span className="inline-flex items-center gap-2 text-[11px] text-muted-foreground" data-slot="log-current">
            <StatusDot status={overridden ? "BLOCKED" : "COMPLETED"} />
            <span className="font-mono text-foreground/85">{state.level}</span>
            {state.expires_at && <span>until {clock(state.expires_at)}</span>}
            {overridden && (
              <Button variant="outline" size="sm" className="h-7 px-2 text-xs" disabled={busy}
                onClick={() => put({ level: state.baseline, ttl_seconds: 0 }, `Back to ${state.baseline}`)}>
                Back to {state.baseline}
              </Button>
            )}
          </span>
        ) : <span className="text-[11px] text-muted-foreground">Not reported</span>}
      </SettingsRow>
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        <Segmented label="Level" options={LEVELS.map((l) => ({ value: l, label: l }))} value={level} onChange={setLevel} disabled={busy} />
        <span className="text-[11px] text-muted-foreground">for</span>
        <Segmented label="Duration" options={DURATIONS.map((d) => ({ value: d.seconds, label: d.label }))} value={ttl} onChange={setTtl} disabled={busy} />
        <Button size="sm" className="h-7 px-2.5 text-xs" disabled={busy}
          onClick={() => put({ level, ttl_seconds: ttl }, `Log level ${level} for ${DURATIONS.find((d) => d.seconds === ttl)?.label}`)}>
          {busy && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}Apply
        </Button>
      </div>
    </SettingsCard>
  )
}

interface OrphanReport {
  orphans: { crew_id: string; slug: string; container_id: string; reaped: boolean }[]
  count: number
  applied: boolean
  inspected: number
  identified: number
  detector_inert: boolean
}

const PROVIDER_ONLY = "Only available with the Docker provider."

function MaintenanceCard({ workspaceId }: { workspaceId: string }) {
  const ws = encodeURIComponent(workspaceId)
  const [busy, setBusy] = React.useState<"check" | "reap" | "legacy" | "prune" | null>(null)
  const [orphans, setOrphans] = React.useState<OrphanReport | null>(null)
  const [legacy, setLegacy] = React.useState<boolean | null>(null)
  const [result, setResult] = React.useState<string | null>(null)
  const [confirmOpen, setConfirmOpen] = React.useState(false)
  const [typed, setTyped] = React.useState("")

  React.useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const res = await apiFetch(`/api/v1/admin/legacy-resources?workspace_id=${ws}`)
        const body = res?.ok ? await res.json() : null
        if (!cancelled) setLegacy(body ? !!body.present : false)
      } catch {
        if (!cancelled) setLegacy(false)
      }
    })()
    return () => { cancelled = true }
  }, [ws])

  /** POST an admin action; returns the body or null after saying why. */
  const post = async <T,>(path: string, fallback: string): Promise<T | null> => {
    try {
      const res = await apiFetch(`${path}${path.includes("?") ? "&" : "?"}workspace_id=${ws}`, { method: "POST" })
      if (res.status === 503) { toast.error(PROVIDER_ONLY); setResult(PROVIDER_ONLY); return null }
      if (!res.ok) { const m = await readApiError(res, fallback); toast.error(m); setResult(m); return null }
      return (await res.json()) as T
    } catch {
      toast.error(fallback)
      setResult(fallback)
      return null
    }
  }

  const check = async () => {
    setBusy("check")
    const r = await post<OrphanReport>("/api/v1/admin/reap-orphan-containers", "The check did not run")
    if (r) {
      setOrphans(r)
      setResult(r.detector_inert
        ? `Inspected ${r.inspected} containers, but none reports its token fingerprint — the check cannot tell orphans apart yet.`
        : r.count === 0 ? `No orphaned containers among ${r.inspected} inspected.` : `${r.count} orphaned container${r.count === 1 ? "" : "s"} found.`)
    }
    setBusy(null)
  }
  const reap = async () => {
    setBusy("reap")
    const r = await post<OrphanReport>("/api/v1/admin/reap-orphan-containers?apply=true", "The containers were not removed")
    if (r) {
      const n = r.orphans.filter((o) => o.reaped).length
      setOrphans(r)
      setResult(`Removed ${n} of ${r.count} orphaned container${r.count === 1 ? "" : "s"}.`)
      toast.success(`Removed ${n} container${n === 1 ? "" : "s"}`)
    }
    setBusy(null)
  }
  const pruneLegacy = async () => {
    setBusy("legacy")
    const r = await post<{ removed: string[]; count: number }>("/api/v1/admin/prune-legacy-resources", "Legacy resources were not removed")
    if (r) {
      setLegacy(false)
      setResult(`Removed ${r.count} legacy resource${r.count === 1 ? "" : "s"}.`)
      toast.success("Legacy resources removed")
    }
    setBusy(null)
  }
  const pruneCrews = async () => {
    setBusy("prune")
    const r = await post<{ removed: string[]; count: number }>("/api/v1/admin/prune-crew-runtimes", "Crew runtimes were not removed")
    if (r) {
      setResult(`Removed the runtime of ${r.count} crew${r.count === 1 ? "" : "s"}. Images were kept.`)
      toast.success("Crew runtimes removed")
      setConfirmOpen(false)
      setTyped("")
    }
    setBusy(null)
  }

  return (
    <SettingsCard icon={Wrench} tint="var(--warn)" title="Maintenance" description="Clean-up the server can do for you. Each action says what it will remove first.">
      <SettingsRow label="Orphaned containers"
        description="Crew containers still holding a token from before the master key was rotated. Checking changes nothing.">
        <span className="flex flex-wrap items-center justify-end gap-1.5">
          <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" disabled={busy !== null} onClick={check}>
            {busy === "check" && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}Check
          </Button>
          {orphans && !orphans.applied && orphans.count > 0 && (
            <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs text-destructive" disabled={busy !== null} onClick={reap}>
              {busy === "reap" && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}Remove {orphans.count}
            </Button>
          )}
        </span>
      </SettingsRow>
      {orphans && orphans.orphans.length > 0 && (
        <ul className="border-b border-border px-4 pb-2.5 text-[11px] text-muted-foreground" data-slot="orphan-list">
          {orphans.orphans.map((o) => (
            <li key={o.container_id} className="flex items-center gap-2 font-mono">
              <StatusDot status={o.reaped ? "COMPLETED" : "BLOCKED"} />{o.slug} · {o.container_id.slice(0, 12)}
            </li>
          ))}
        </ul>
      )}
      {legacy && (
        <SettingsRow label="Legacy resources" description="Containers and volumes left by an older Crewship naming scheme. Nothing current uses them.">
          <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" disabled={busy !== null} onClick={pruneLegacy} data-testid="legacy-remove">
            {busy === "legacy" && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}Remove
          </Button>
        </SettingsRow>
      )}
      <div className="mx-4 my-3 flex flex-wrap items-center gap-3 rounded-lg border border-destructive/35 bg-destructive/[0.04] px-3 py-2.5" data-slot="runtime-danger">
        <div className="min-w-0 flex-1">
          <p className="text-xs font-medium text-destructive">Remove every crew runtime in this workspace</p>
          <p className="text-[11px] text-muted-foreground">Stops and deletes each crew&apos;s containers and volumes. Cached images stay, so crews rebuild quickly on their next run.</p>
        </div>
        <Button variant="outline" size="sm" className="h-7 border-destructive/40 px-2.5 text-xs text-destructive" disabled={busy !== null} onClick={() => setConfirmOpen(true)}>
          <Trash2 className="mr-1.5 h-3 w-3" />Remove runtimes…
        </Button>
      </div>
      {result && <p className="px-4 pb-3 text-[11px] text-muted-foreground motion-safe:animate-in motion-safe:fade-in-0" role="status" data-slot="maintenance-result">{result}</p>}

      <AlertDialog open={confirmOpen} onOpenChange={(v) => { if (busy !== "prune") { setConfirmOpen(v); if (!v) setTyped("") } }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2"><Trash2 className="h-4 w-4 text-destructive" />Remove every crew runtime?</AlertDialogTitle>
            <AlertDialogDescription>
              Every crew in this workspace loses its running containers and volumes. Work in progress inside them is gone; cached images are kept.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="prune-confirm" className="text-xs">Type <span className="font-mono">remove</span> to confirm</Label>
            <Input id="prune-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy === "prune"}>Cancel</AlertDialogCancel>
            {/* Plain Button, not AlertDialogAction: the action closes the dialog
                before the request answers. */}
            <Button variant="destructive" disabled={typed.trim().toLowerCase() !== "remove" || busy === "prune"} onClick={pruneCrews}>
              {busy === "prune" && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}Remove runtimes
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsCard>
  )
}
