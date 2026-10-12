"use client"

import * as React from "react"
import { Bot, Globe, Lock, ShieldAlert, Terminal } from "lucide-react"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"

import {
  HOOK_EVENTS,
  HOOK_GROUPS,
  SEVERITIES,
  draftFrom,
  draftProblems,
  eventInfo,
  writeBody,
  type HookDraft,
  type HookRow,
} from "./hooks-model"

const stepHead = "flex items-center gap-2 text-control font-semibold"
const stepNo = "grid h-5 w-5 shrink-0 place-items-center rounded-full bg-primary text-[10.5px] font-bold text-primary-foreground"

/**
 * New hook / edit hook: three numbered steps in one dialog — when, do what,
 * details — in plain words first and the event id underneath.
 *
 * What it offers follows what the server will accept and actually run:
 *   - HTTP for OWNER and ADMIN (the route is roleManage);
 *   - Shell only for OWNER (validateHandlerKind), with the warning that it
 *     runs on the Crewship host outside any container;
 *   - "Hand to an agent" is shown but not selectable: the subagent handler
 *     is never wired in this build (hooks.SetSubagentHandler has no caller),
 *     so such a hook could only ever record "subagent handler not
 *     configured".
 * Blocking is offered only on events whose call site can still cancel.
 */
export function HookEditor({
  open,
  onOpenChange,
  workspaceId,
  role,
  crews,
  editing,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  role: string | null | undefined
  crews: { id: string; name: string }[]
  editing?: HookRow | null
  onSaved: (hook: HookRow | null) => void
}) {
  const [d, setD] = React.useState<HookDraft>(() => draftFrom(editing ?? undefined))
  const [saving, setSaving] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [touched, setTouched] = React.useState(false)
  React.useEffect(() => {
    if (open) { setD(draftFrom(editing ?? undefined)); setError(null); setTouched(false) }
  }, [open, editing])

  const isOwner = role === "OWNER"
  const set = (patch: Partial<HookDraft>) => setD((prev) => ({ ...prev, ...patch }))
  const info = d.event ? eventInfo(d.event) : null
  const problems = draftProblems(d)
  // An edit of someone else's shell hook by an ADMIN would be refused; say so
  // rather than offering a Save that 403s.
  const shellLocked = d.kind === "shell" && !isOwner

  async function save() {
    setTouched(true)
    if (problems.length || shellLocked) return
    setSaving(true)
    setError(null)
    try {
      const ws = encodeURIComponent(workspaceId)
      const res = await apiFetch(editing ? `/api/v1/hooks/${encodeURIComponent(editing.id)}?workspace_id=${ws}` : `/api/v1/hooks?workspace_id=${ws}`, {
        method: editing ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(writeBody(d, editing ?? undefined)),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) {
        setError(res.status === 403 ? "Your role cannot save this hook." : (body?.detail ?? body?.error ?? `Saving failed (${res.status}).`))
        return
      }
      onSaved((body as HookRow | null) ?? null)
      onOpenChange(false)
    } catch {
      setError("Network error — nothing was saved.")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92dvh] gap-0 overflow-hidden p-0 sm:max-w-2xl">
        <DialogHeader className="border-b border-border px-5 py-4">
          <DialogTitle className="text-base">{editing ? "Edit hook" : "New hook"}</DialogTitle>
          <DialogDescription className="text-xs">Code this workspace runs when something happens on the platform.</DialogDescription>
        </DialogHeader>

        <div className="max-h-[calc(92dvh-9rem)] space-y-6 overflow-y-auto px-5 py-5">
          {/* 1 · When */}
          <section aria-label="When">
            <h3 className={stepHead}><span className={stepNo}>1</span>When</h3>
            <div className="mt-3 space-y-3">
              {HOOK_GROUPS.map((g) => (
                <div key={g.id}>
                  <div className="mb-1.5 font-mono text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground-soft">{g.label}</div>
                  <div role="radiogroup" aria-label={g.label} className="grid gap-1.5 sm:grid-cols-2">
                    {HOOK_EVENTS.filter((e) => e.group === g.id).map((e) => {
                      const checked = d.event === e.id
                      return (
                        <button key={e.id} type="button" role="radio" aria-checked={checked} aria-label={e.label} onClick={() => set({ event: e.id, blocking: e.canBlock ? d.blocking : false })}
                          className={cn("kit-tap flex flex-col items-start gap-0.5 rounded-lg border px-3 py-2 text-left transition-colors",
                            checked ? "border-primary bg-primary/5 ring-1 ring-primary" : "border-border hover:border-line-strong")}>
                          <span className="flex items-center gap-1.5 text-control">
                            {e.label}
                            {e.canBlock && <span className="rounded bg-warn/10 px-1 text-[9.5px] font-semibold uppercase tracking-wide text-warn">can block</span>}
                          </span>
                          <span className="font-mono text-micro text-muted-foreground">{e.id}</span>
                        </button>
                      )
                    })}
                  </div>
                </div>
              ))}
            </div>
          </section>

          {/* 2 · Do what */}
          <section aria-label="Do what">
            <h3 className={stepHead}><span className={stepNo}>2</span>Do what</h3>
            <div role="radiogroup" aria-label="Handler" className="mt-3 grid gap-2 sm:grid-cols-3">
              <HandlerOption checked={d.kind === "http"} onPick={() => set({ kind: "http" })} icon={Globe} title="Send a webhook"
                body="POST the event as JSON to a URL — Slack, PagerDuty, your own API." />
              <HandlerOption checked={false} disabled icon={Bot} title="Hand to an agent"
                body="Not available in this build yet." note="Coming later" />
              <HandlerOption checked={d.kind === "shell"} disabled={!isOwner} onPick={() => set({ kind: "shell" })} icon={Terminal} title="Run a command on the server" danger
                body={isOwner ? "Runs on the Crewship host, outside any container. Owner only." : "Only the workspace owner can add a shell hook."} />
            </div>
          </section>

          {/* 3 · Details */}
          <section aria-label="Details">
            <h3 className={stepHead}><span className={stepNo}>3</span>Details</h3>
            <div className="mt-3 space-y-4">
              {d.kind === "http" ? (
                <>
                  <Field label="URL" hint="Receives a POST with the event JSON. 2xx passes, 4xx/5xx blocks (on events that can block).">
                    <Input value={d.url} onChange={(e) => set({ url: e.target.value })} placeholder="https://hooks.slack.com/services/…" className="font-mono text-xs" aria-label="URL" />
                  </Field>
                  <Field label="Signing secret (optional)" hint={editing?.handler_config?.secret ? "A secret is set; leave empty to keep it." : "Signs each request (X-Crewship-Signature, HMAC-SHA256) so the receiver can verify it."}>
                    <Input value={d.secret} onChange={(e) => set({ secret: e.target.value })} type="password" autoComplete="off" aria-label="Signing secret" />
                  </Field>
                </>
              ) : (
                <Field label="Command" hint="sh -c on the host, 30 s timeout, event details in CREWSHIP_* variables.">
                  <Input value={d.command} onChange={(e) => set({ command: e.target.value })} placeholder="/usr/local/bin/notify.sh" className="font-mono text-xs" aria-label="Command" disabled={!isOwner} />
                </Field>
              )}

              <Field label="Scope" hint="A crew-scoped hook only runs for that crew's events.">
                <select value={d.crewId} onChange={(e) => set({ crewId: e.target.value })} aria-label="Scope"
                  className="h-9 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control">
                  <option value="">Whole workspace</option>
                  {crews.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </select>
              </Field>

              {info?.hasSeverity && (
                <Field label="Only at these severities (optional)">
                  <div className="flex flex-wrap gap-1.5" role="group" aria-label="Severities">
                    {SEVERITIES.map((s) => {
                      const on = d.severities.includes(s)
                      return (
                        <button key={s} type="button" aria-pressed={on} onClick={() => set({ severities: on ? d.severities.filter((x) => x !== s) : [...d.severities, s] })}
                          className={cn("h-7 rounded-full border px-2.5 text-xs capitalize", on ? "border-primary bg-primary/10 text-primary-hover" : "border-border text-muted-foreground")}>
                          {s}
                        </button>
                      )
                    })}
                  </div>
                </Field>
              )}
              {info?.hasTool && (
                <Field label="Only for these tools (optional)" hint="Comma-separated names or regular expressions, e.g. Bash, Write.*">
                  <Input value={d.tools} onChange={(e) => set({ tools: e.target.value })} className="font-mono text-xs" aria-label="Tools" />
                </Field>
              )}

              {info?.canBlock && (
                <label className="flex items-start justify-between gap-4 rounded-lg border border-warn/30 bg-warn/5 px-3 py-2.5">
                  <span className="text-xs">
                    <span className="block font-medium text-foreground">Block the action when the hook says so</span>
                    <span className="text-muted-foreground">The {info.label.toLowerCase()} waits for the hook; a 4xx/5xx (or non-zero exit) cancels it.</span>
                  </span>
                  <Switch checked={d.blocking} onCheckedChange={(v) => set({ blocking: v })} aria-label="Block the action" />
                </label>
              )}
              <label className="flex items-center justify-between gap-4 text-xs">
                <span>Enabled</span>
                <Switch checked={d.enabled} onCheckedChange={(v) => set({ enabled: v })} aria-label="Enabled" />
              </label>
            </div>
          </section>

          {touched && problems.length > 0 && (
            <ul role="alert" className="list-disc space-y-0.5 rounded-lg border border-destructive/30 bg-destructive/5 py-2 pl-7 pr-3 text-xs text-destructive">
              {problems.map((p) => <li key={p}>{p}</li>)}
            </ul>
          )}
          {error && <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">{error}</p>}
        </div>

        <DialogFooter className="border-t border-border px-5 py-3">
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={saving}>Cancel</Button>
          <Button size="sm" onClick={() => void save()} disabled={saving || shellLocked}>{editing ? "Save hook" : "Create hook"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium">{label}</div>
      {children}
      {hint && <p className="text-label text-muted-foreground">{hint}</p>}
    </div>
  )
}

function HandlerOption({ checked, disabled, onPick, icon: Icon, title, body, note, danger }: {
  checked: boolean; disabled?: boolean; onPick?: () => void; icon: React.ElementType; title: string; body: string; note?: string; danger?: boolean
}) {
  return (
    <button type="button" role="radio" aria-checked={checked} aria-label={title} disabled={disabled} onClick={onPick}
      className={cn("flex flex-col items-start gap-1 rounded-xl border p-3 text-left transition-colors disabled:cursor-not-allowed",
        checked ? "border-primary bg-primary/5 ring-1 ring-primary" : danger ? "border-destructive/30 hover:border-destructive/50" : "border-border hover:border-line-strong",
        disabled && !checked && "opacity-55")}>
      <span className="flex w-full items-center gap-2 text-control font-medium">
        <Icon className={cn("h-4 w-4", danger ? "text-destructive" : "text-muted-foreground")} aria-hidden />
        {title}
        {note && <span className="ml-auto rounded bg-muted px-1.5 text-[10px] font-normal text-muted-foreground">{note}</span>}
        {danger && <ShieldAlert className="ml-auto h-3.5 w-3.5 text-destructive" aria-hidden />}
        {disabled && !note && !danger && <Lock className="ml-auto h-3 w-3" aria-hidden />}
      </span>
      <span className={cn("text-label leading-snug", danger ? "text-destructive/90" : "text-muted-foreground")}>{body}</span>
    </button>
  )
}
