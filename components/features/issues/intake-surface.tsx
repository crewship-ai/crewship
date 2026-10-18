"use client"

import * as React from "react"
import { Inbox, Play, Plus, Repeat, Trash2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { TabBar } from "@/components/ui/tab-bar"
import { nativeSelect } from "@/components/features/settings/shared"
import { PRIORITY_TEXT } from "./issues-view-model"

/**
 * Intake: how work arrives without a person typing an issue. Triage rules
 * sort new issues by their title; recurring issues stamp a fresh issue on a
 * cron schedule. Both sit on routes that already exist
 * (/triage-rules, /triage/process, /recurring-issues) and the CLI commands
 * `crewship triage` and `crewship recurring`.
 */
export type IntakeTab = "triage" | "recurring"

export interface TriageRule {
  id: string
  name: string
  pattern: string
  match_type: "contains" | "regex" | "exact" | string
  crew_id: string | null
  assignee_id: string | null
  priority: string | null
  project_id: string | null
  labels_json: string | null
  position: number
  enabled: boolean
  match_count: number
}

export interface RecurringIssue {
  id: string
  crew_id: string
  crew_name?: string
  title: string
  priority: string
  cron_expression: string
  enabled: boolean
  next_run: string | null
  last_run: string | null
  run_count: number
}

interface Named {
  id: string
  name: string
}

/** "Title contains “bug”" — what a rule matches, in words. */
export function describeRule(r: Pick<TriageRule, "match_type" | "pattern">): string {
  if (r.match_type === "exact") return `Title is “${r.pattern}”`
  if (r.match_type === "regex") return `Title matches /${r.pattern}/`
  return `Title contains “${r.pattern}”`
}

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]

/** Names the schedules people write most (daily, weekly at a fixed time); anything else stays cron. */
export function describeCron(expr: string): string {
  const m = /^(\d{1,2}) (\d{1,2}) \* \* (\*|[0-6])$/.exec(expr.trim())
  if (m) {
    const time = `${m[2].padStart(2, "0")}:${m[1].padStart(2, "0")} UTC`
    return m[3] === "*" ? `Every day at ${time}` : `Every ${DAYS[Number(m[3])]} at ${time}`
  }
  return `${expr.trim()} (UTC)`
}

async function reason(res: Response): Promise<string> {
  const b = (await res.json().catch(() => null)) as { detail?: string; error?: string } | null
  return b?.detail ?? b?.error ?? `The server refused the change (HTTP ${res.status})`
}

/** One list endpoint: items, a load error, and a reload. */
function useIntakeList<T>(path: string, workspaceId: string) {
  const [state, setState] = React.useState<{ items: T[] | null; error: string | null }>({ items: null, error: null })
  const url = `/api/v1/${path}?workspace_id=${encodeURIComponent(workspaceId)}`
  const load = React.useCallback(async () => {
    try {
      const res = await apiFetch(url)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const body = (await res.json()) as unknown
      setState({ items: Array.isArray(body) ? (body as T[]) : [], error: null })
    } catch (e) {
      setState({ items: null, error: e instanceof Error ? e.message : "no connection" })
    }
  }, [url])
  React.useEffect(() => {
    setState({ items: null, error: null })
    void load()
  }, [load])
  return { ...state, reload: load, setItems: (f: (items: T[]) => T[]) => setState((s) => (s.items ? { ...s, items: f(s.items) } : s)) }
}

function useNamed(path: string, workspaceId: string): Named[] {
  const [list, setList] = React.useState<Named[]>([])
  React.useEffect(() => {
    let live = true
    apiFetch(`/api/v1/${path}?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((b: unknown) => {
        const rows = Array.isArray(b) ? b : ((b as Record<string, unknown>)?.[path] as unknown[] | undefined) ?? []
        if (live) setList((rows as Named[]).map((x) => ({ id: x.id, name: x.name })))
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [path, workspaceId])
  return list
}

export function IntakeSurface({ workspaceId, tab, onTabChange }: { workspaceId: string; tab: IntakeTab; onTabChange: (t: IntakeTab) => void }) {
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-4 py-5 sm:px-6">
      <header className="grid gap-1">
        <h2 className="text-display font-bold tracking-tight">Intake</h2>
        <p className="text-body text-muted-foreground">Work that arrives on its own: rules that sort new issues, and schedules that open one.</p>
      </header>
      <TabBar value={tab} onValueChange={(v) => onTabChange(v as IntakeTab)} ariaLabel="Intake" layoutId="intake-tabs" className="overflow-x-auto [scrollbar-width:none]">
        <TabBar.Item value="triage">Triage rules</TabBar.Item>
        <TabBar.Item value="recurring">Recurring issues</TabBar.Item>
      </TabBar>
      <div role="tabpanel" aria-label={tab === "triage" ? "Triage rules" : "Recurring issues"}>
        {tab === "triage" ? <TriagePanel workspaceId={workspaceId} /> : <RecurringPanel workspaceId={workspaceId} />}
      </div>
    </div>
  )
}

/* ------------------------------------------------------------- shared bits */

function Row({ children }: { children: React.ReactNode }) {
  return (
    <li data-slot="intake-row" className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border px-4 py-3 last:border-b-0">
      {children}
    </li>
  )
}

function LoadError({ what, onRetry }: { what: string; onRetry: () => void }) {
  return (
    <div role="alert" className="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-label text-destructive">
      <span className="min-w-0 flex-1">Could not load {what}. What you see may be out of date.</span>
      <Button size="sm" variant="outline" onClick={onRetry}>
        Try again
      </Button>
    </div>
  )
}

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="text-label text-muted-foreground">{hint}</p>}
    </div>
  )
}

/** PATCH / DELETE with the server's reason on failure. */
async function send(method: "PATCH" | "DELETE" | "POST", url: string, body?: unknown): Promise<Response> {
  const res = await apiFetch(url, {
    method,
    ...(body !== undefined ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : {}),
  })
  if (!res.ok) throw new Error(await reason(res))
  return res
}

const priorityName = (p: string | null | undefined) => (p && p !== "none" ? `${PRIORITY_TEXT[p as keyof typeof PRIORITY_TEXT] ?? p} priority` : null)

/* ---------------------------------------------------------------- triage */

function TriagePanel({ workspaceId }: { workspaceId: string }) {
  const ws = encodeURIComponent(workspaceId)
  const { items, error, reload, setItems } = useIntakeList<TriageRule>("triage-rules", workspaceId)
  const crews = useNamed("crews", workspaceId)
  const [creating, setCreating] = React.useState(false)
  const [doomed, setDoomed] = React.useState<TriageRule | null>(null)
  const [running, setRunning] = React.useState(false)

  const toggle = async (r: TriageRule, enabled: boolean) => {
    setItems((l) => l.map((x) => (x.id === r.id ? { ...x, enabled } : x)))
    try {
      await send("PATCH", `/api/v1/triage-rules/${encodeURIComponent(r.id)}?workspace_id=${ws}`, { enabled })
    } catch (e) {
      setItems((l) => l.map((x) => (x.id === r.id ? { ...x, enabled: r.enabled } : x)))
      toast.error(e instanceof Error ? e.message : "Could not change the rule")
    }
  }

  const run = async () => {
    setRunning(true)
    try {
      const res = await send("POST", `/api/v1/triage/process?workspace_id=${ws}`)
      const b = (await res.json().catch(() => ({}))) as { processed?: number; matched?: number }
      toast.success(`Triage ran: ${b.matched ?? 0} of ${b.processed ?? 0} pending issues matched a rule`)
      void reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Triage could not run")
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <p className="min-w-[14rem] flex-1 text-body text-muted-foreground">The first enabled rule that matches an issue’s title decides where it goes. Rules run in order.</p>
        <Button variant="outline" disabled={running} onClick={() => void run()}>
          <Play className="size-3.5" aria-hidden />
          Run triage now
        </Button>
        <Button onClick={() => setCreating(true)}>
          <Plus className="size-3.5" aria-hidden />
          New rule
        </Button>
      </div>
      {error && <LoadError what="the triage rules" onRetry={() => void reload()} />}
      {items === null && !error && <p className="text-label text-muted-foreground">Loading triage rules…</p>}
      {items?.length === 0 && (
        <InlineEmpty icon={Inbox} text="No triage rules yet. A rule sends matching new issues to a crew, an agent or a priority." />
      )}
      {items && items.length > 0 && (
        <ul className="rounded-card border border-border bg-card">
          {items.map((r) => {
            const doing = [r.crew_id ? `Crew ${crews.find((c) => c.id === r.crew_id)?.name ?? "—"}` : null, priorityName(r.priority)].filter(Boolean)
            return (
              <Row key={r.id}>
                <div className="min-w-0 flex-1 basis-56">
                  <b className="block truncate text-body font-semibold">{r.name}</b>
                  <span className="block truncate text-label text-muted-foreground">{describeRule(r)}</span>
                  {doing.length > 0 && <span className="block truncate text-label text-muted-foreground">{doing.join(" · ")}</span>}
                </div>
                <span className="font-mono text-label text-muted-foreground-soft">
                  {r.match_count} {r.match_count === 1 ? "match" : "matches"}
                </span>
                <Switch checked={r.enabled} onCheckedChange={(v) => void toggle(r, v)} aria-label={`Enable rule ${r.name}`} />
                <Button variant="ghost" size="icon-xs" aria-label={`Delete rule ${r.name}`} onClick={() => setDoomed(r)}>
                  <Trash2 className="size-3.5" aria-hidden />
                </Button>
              </Row>
            )
          })}
        </ul>
      )}

      {creating && <NewRuleDialog workspaceId={workspaceId} crews={crews} onClose={() => setCreating(false)} onCreated={() => void reload()} />}
      <ConfirmDialog
        open={doomed !== null}
        onOpenChange={(o) => !o && setDoomed(null)}
        title={`Delete rule “${doomed?.name ?? ""}”?`}
        description="Issues it already sorted stay where they are."
        consequences={[{ tone: "lost", text: "The rule stops applying to new issues." }]}
        confirmLabel="Delete rule"
        destructive
        onConfirm={async () => {
          if (!doomed) return
          try {
            await send("DELETE", `/api/v1/triage-rules/${encodeURIComponent(doomed.id)}?workspace_id=${ws}`)
            setItems((l) => l.filter((x) => x.id !== doomed.id))
          } catch (e) {
            toast.error(e instanceof Error ? e.message : "Could not delete the rule")
          }
        }}
      />
    </div>
  )
}

function NewRuleDialog({ workspaceId, crews, onClose, onCreated }: { workspaceId: string; crews: Named[]; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = React.useState("")
  const [pattern, setPattern] = React.useState("")
  const [matchType, setMatchType] = React.useState("contains")
  const [priority, setPriority] = React.useState("")
  const [crew, setCrew] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)
  const ready = name.trim() !== "" && pattern.trim() !== ""

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    setErr(null)
    try {
      await send("POST", `/api/v1/triage-rules?workspace_id=${encodeURIComponent(workspaceId)}`, {
        name: name.trim(),
        pattern: pattern.trim(),
        match_type: matchType,
        ...(priority ? { priority } : {}),
        ...(crew ? { crew_id: crew } : {}),
      })
      onCreated()
      onClose()
    } catch (e2) {
      setErr(e2 instanceof Error ? e2.message : "Could not create the rule")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>New triage rule</DialogTitle>
            <DialogDescription>Match new issues by title, then say where they go.</DialogDescription>
          </DialogHeader>
          <Field id="tr-name" label="Name">
            <Input id="tr-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="tr-type" label="Match">
              <select id="tr-type" value={matchType} onChange={(e) => setMatchType(e.target.value)} className={nativeSelect}>
                <option value="contains">Title contains</option>
                <option value="exact">Title is exactly</option>
                <option value="regex">Title matches (regex)</option>
              </select>
            </Field>
            <Field id="tr-pattern" label="Title pattern">
              <Input id="tr-pattern" value={pattern} onChange={(e) => setPattern(e.target.value)} />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="tr-crew" label="Send to crew">
              <select id="tr-crew" value={crew} onChange={(e) => setCrew(e.target.value)} className={nativeSelect}>
                <option value="">Leave as is</option>
                {crews.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field id="tr-priority" label="Set priority">
              <select id="tr-priority" value={priority} onChange={(e) => setPriority(e.target.value)} className={nativeSelect}>
                <option value="">Leave as is</option>
                {(["urgent", "high", "medium", "low"] as const).map((p) => (
                  <option key={p} value={p}>
                    {PRIORITY_TEXT[p]}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          {err && (
            <p role="alert" className="text-label text-destructive">
              {err}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!ready || busy}>
              Create rule
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ------------------------------------------------------------- recurring */

function RecurringPanel({ workspaceId }: { workspaceId: string }) {
  const ws = encodeURIComponent(workspaceId)
  const { items, error, reload, setItems } = useIntakeList<RecurringIssue>("recurring-issues", workspaceId)
  const crews = useNamed("crews", workspaceId)
  const [creating, setCreating] = React.useState(false)
  const [doomed, setDoomed] = React.useState<RecurringIssue | null>(null)

  const toggle = async (r: RecurringIssue, enabled: boolean) => {
    setItems((l) => l.map((x) => (x.id === r.id ? { ...x, enabled } : x)))
    try {
      await send("PATCH", `/api/v1/recurring-issues/${encodeURIComponent(r.id)}?workspace_id=${ws}`, { enabled })
    } catch (e) {
      setItems((l) => l.map((x) => (x.id === r.id ? { ...x, enabled: r.enabled } : x)))
      toast.error(e instanceof Error ? e.message : "Could not change the schedule")
    }
  }

  const when = (iso: string | null) =>
    iso ? new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }) : null

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <p className="min-w-[14rem] flex-1 text-body text-muted-foreground">Each tick opens a fresh Backlog issue with the same title, crew and priority.</p>
        <Button onClick={() => setCreating(true)}>
          <Plus className="size-3.5" aria-hidden />
          New schedule
        </Button>
      </div>
      {error && <LoadError what="the recurring issues" onRetry={() => void reload()} />}
      {items === null && !error && <p className="text-label text-muted-foreground">Loading recurring issues…</p>}
      {items?.length === 0 && <InlineEmpty icon={Repeat} text="No recurring issues yet. Use one for the weekly review or the daily digest you would otherwise retype." />}
      {items && items.length > 0 && (
        <ul className="rounded-card border border-border bg-card">
          {items.map((r) => (
            <Row key={r.id}>
              <div className="min-w-0 flex-1 basis-56">
                <b className="block truncate text-body font-semibold">{r.title}</b>
                <span className="block truncate text-label text-muted-foreground">{describeCron(r.cron_expression)}</span>
                <span className="block truncate text-label text-muted-foreground">
                  {[r.crew_name ?? crews.find((c) => c.id === r.crew_id)?.name, r.enabled && when(r.next_run) ? `next run ${when(r.next_run)}` : r.enabled ? null : "paused"]
                    .filter(Boolean)
                    .join(" · ")}
                </span>
              </div>
              <span className="font-mono text-label text-muted-foreground-soft">
                ran {r.run_count} {r.run_count === 1 ? "time" : "times"}
              </span>
              <Switch checked={r.enabled} onCheckedChange={(v) => void toggle(r, v)} aria-label={`Enable schedule ${r.title}`} />
              <Button variant="ghost" size="icon-xs" aria-label={`Delete schedule ${r.title}`} onClick={() => setDoomed(r)}>
                <Trash2 className="size-3.5" aria-hidden />
              </Button>
            </Row>
          ))}
        </ul>
      )}

      {creating && <NewScheduleDialog workspaceId={workspaceId} crews={crews} onClose={() => setCreating(false)} onCreated={() => void reload()} />}
      <ConfirmDialog
        open={doomed !== null}
        onOpenChange={(o) => !o && setDoomed(null)}
        title={`Delete schedule “${doomed?.title ?? ""}”?`}
        description="Issues it already opened stay."
        consequences={[{ tone: "lost", text: "No new issues are opened from it." }]}
        confirmLabel="Delete schedule"
        destructive
        onConfirm={async () => {
          if (!doomed) return
          try {
            await send("DELETE", `/api/v1/recurring-issues/${encodeURIComponent(doomed.id)}?workspace_id=${ws}`)
            setItems((l) => l.filter((x) => x.id !== doomed.id))
          } catch (e) {
            toast.error(e instanceof Error ? e.message : "Could not delete the schedule")
          }
        }}
      />
    </div>
  )
}

function NewScheduleDialog({ workspaceId, crews, onClose, onCreated }: { workspaceId: string; crews: Named[]; onClose: () => void; onCreated: () => void }) {
  const [crew, setCrew] = React.useState("")
  const [title, setTitle] = React.useState("")
  const [cron, setCron] = React.useState("0 9 * * *")
  const [priority, setPriority] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)
  const ready = crew !== "" && title.trim() !== "" && cron.trim() !== ""

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    setErr(null)
    try {
      await send("POST", `/api/v1/recurring-issues?workspace_id=${encodeURIComponent(workspaceId)}`, {
        crew_id: crew,
        title: title.trim(),
        cron_expression: cron.trim(),
        ...(priority ? { priority } : {}),
      })
      onCreated()
      onClose()
    } catch (e2) {
      setErr(e2 instanceof Error ? e2.message : "Could not create the schedule")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>New recurring issue</DialogTitle>
            <DialogDescription>Opens a Backlog issue in the crew on every tick.</DialogDescription>
          </DialogHeader>
          <Field id="ri-crew" label="Crew">
            <select id="ri-crew" value={crew} onChange={(e) => setCrew(e.target.value)} className={nativeSelect}>
              <option value="">Choose a crew</option>
              {crews.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </Field>
          <Field id="ri-title" label="Title">
            <Input id="ri-title" value={title} onChange={(e) => setTitle(e.target.value)} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="ri-cron" label="Cron (UTC)" hint={describeCron(cron)}>
              <Input id="ri-cron" value={cron} onChange={(e) => setCron(e.target.value)} className={cn("font-mono")} />
            </Field>
            <Field id="ri-priority" label="Priority">
              <select id="ri-priority" value={priority} onChange={(e) => setPriority(e.target.value)} className={nativeSelect}>
                <option value="">No priority</option>
                {(["urgent", "high", "medium", "low"] as const).map((p) => (
                  <option key={p} value={p}>
                    {PRIORITY_TEXT[p]}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          {err && (
            <p role="alert" className="text-label text-destructive">
              {err}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!ready || busy}>
              Create schedule
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
