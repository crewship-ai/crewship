"use client"

import * as React from "react"
import { AlertTriangle, ArrowDownToLine, Circle, Copy, FileText, Globe, Link2, Play, Repeat, RotateCw, Shield, Terminal, Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { StatusPill } from "@/components/ui/status-pill"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { PageEmpty, PageError, PageLoading, PageNoResults, PageNotFound } from "@/components/layout/page-state"
import { SettingsCard, SettingsDangerCard, SettingsRow, SettingsSaveBar, nativeSelect } from "@/components/features/settings/shared"
import type { PipelineWebhook } from "@/hooks/use-pipeline-webhooks"
import { formatRelativeShort } from "@/lib/time"
import { IncomingTargetAvatar } from "./incoming-target-avatar"
import type { IncomingKind, IncomingTarget } from "./incoming-model"
import { incomingJSON, type IncomingData } from "./use-incoming-endpoints"
import { RecentReceipts } from "./views/incoming-webhooks-view"
import { absolute, copy, RevealDialog, type Reveal } from "./views/incoming-credentials"
import { triggerCounts, type TriggerRow, type TriggerView } from "./triggers-model"
import { MappingEditor, mappingProblem, mappingRows, mappingTemplate, sameMapping, type MappingRow } from "./trigger-mapping"
import { Banner, CellMain, CliLines, FlowSteps, GlyphTile, HealthStrip, InfoCard, MetaChip, ObjectPage, ResponsiveList, SpaceHead } from "./integrations-ui"

/**
 * Triggers (Harbor 2, I-TRIG / I-EP): what starts work from outside.
 *
 * Replay and "Kept for replay" are drawn for an agent's Calls (work item
 * replay route, INT-08). Not drawn, because the server has none: App events
 * (Composio has no receiver yet), Email in, testing a mapping or sending a
 * test call from here (the secret is shown only once; #3478), and pointing a
 * trigger at an agent (PATCH takes another routine only).
 */

export const KIND_LABEL: Record<IncomingKind, { one: string; many: string }> = {
  routine: { one: "Routine", many: "Routines" },
  agent: { one: "Agent", many: "Agents" },
  page: { one: "Artifact", many: "Artifacts" },
}

export function TriggerState({ row }: { row: TriggerRow }) {
  if (!row.enabled) return <StatusPill tone="muted" label="Off" />
  if (row.failing) return <StatusPill tone="danger" label="Failing" />
  return <StatusPill tone="success" label="On" />
}

const VIEW_TITLE: Record<TriggerView, string> = { all: "Triggers", failing: "Failing", off: "Turned off" }

export function TriggersList({
  rows,
  allRows,
  pages,
  view,
  starts,
  filtered,
  loading,
  error,
  onRetry,
  onOpen,
  onOpenTarget,
  onAdd,
  onView,
  onClearFilters,
}: {
  rows: TriggerRow[]
  allRows: TriggerRow[]
  /** Artifacts keep their webhooks per page; listed as targets, not rows. */
  pages: IncomingTarget[]
  view: TriggerView
  starts: IncomingKind | null
  filtered: boolean
  loading: boolean
  error: string | null
  onRetry: () => void
  onOpen: (id: string) => void
  onOpenTarget: (t: IncomingTarget) => void
  onAdd: () => void
  onView: (v: TriggerView) => void
  onClearFilters: () => void
}) {
  const c = triggerCounts(allRows)
  const title = starts ? `Triggers of ${KIND_LABEL[starts].many.toLowerCase()}` : VIEW_TITLE[view]
  const showPages = (starts === null || starts === "page") && view === "all" && pages.length > 0

  let body: React.ReactNode
  if (loading && allRows.length === 0) body = <PageLoading rows={5} />
  else if (error && allRows.length === 0) body = <PageError title="Triggers could not load" description="Tools and Channels are not affected." onRetry={onRetry} detail={error} />
  else if (allRows.length === 0 && pages.length === 0)
    body = (
      <PageEmpty
        icon={ArrowDownToLine}
        title="No triggers yet"
        description="A trigger is a URL another system calls to start a routine, wake an agent or write rows to an artifact."
        primary={{ label: "New trigger", onClick: onAdd }}
        cli="crewship routine webhooks create"
      />
    )
  else if (rows.length === 0 && !showPages) body = <PageNoResults onClear={onClearFilters} />
  else
    body = (
      <>
        {rows.length > 0 && (
          <>
            <p className="text-label text-muted-foreground">{rows.length} {rows.length === 1 ? "trigger" : "triggers"}</p>
            <ResponsiveList
              caption="Triggers"
              columns={[
                { key: "t", label: "Trigger" },
                { key: "s", label: "Starts" },
                { key: "c", label: "Checks the sender" },
                { key: "l", label: "Last call" },
                { key: "x", label: "State" },
              ]}
              items={rows.map((r) => ({
                id: r.id,
                label: `Open ${r.name}`,
                lead: <GlyphTile icon={ArrowDownToLine} size={30} />,
                title: r.name,
                sub: `→ ${KIND_LABEL[r.kind].one} · ${r.target.name}`,
                meta: <><Shield className="size-3" aria-hidden />{r.checks}</>,
                state: <TriggerState row={r} />,
                cells: [
                  <CellMain key="t" lead={<GlyphTile icon={ArrowDownToLine} size={28} />} title={r.name} mono sub={r.calls !== null ? `${r.calls} calls` : "calls not counted"} />,
                  <CellMain key="s" lead={<IncomingTargetAvatar target={r.target} />} title={r.target.name} sub={KIND_LABEL[r.kind].one} />,
                  <span key="c" className="text-label text-muted-foreground">{r.checks}</span>,
                  <span key="l" className="font-mono text-label text-muted-foreground">{r.last ? formatRelativeShort(r.last) : "—"}</span>,
                  <TriggerState key="x" row={r} />,
                ],
              }))}
              onOpen={onOpen}
            />
          </>
        )}
        {showPages && (
          <InfoCard title="Artifacts · webhooks are kept per artifact" icon={FileText}>
            <ul className="divide-y divide-border">
              {pages.map((p) => (
                <li key={p.id}>
                  <button type="button" onClick={() => onOpenTarget(p)} className="kit-tap flex w-full min-w-0 items-center gap-2.5 px-4 py-2 text-left hover:bg-[var(--row-hover-bg)]" aria-label={`Open webhooks of ${p.name}`}>
                    <IncomingTargetAvatar target={p} />
                    <span className="min-w-0 flex-1 truncate text-body text-foreground">{p.name}</span>
                    <span className="text-label text-muted-foreground">Token in the URL</span>
                  </button>
                </li>
              ))}
            </ul>
          </InfoCard>
        )}
      </>
    )

  return (
    <div className="grid min-w-0 gap-3.5">
      <SpaceHead title={title} description="What starts work from outside: a URL another system calls. Each row shows what it starts. Schedules stay on the routine." />
      {!filtered && allRows.length > 0 && (
        <HealthStrip
          label="Triggers at a glance"
          items={[
            { key: "n", tone: "ok", icon: Play, label: "Triggers", value: c.total, sub: `${allRows.filter((r) => r.kind === "routine").length} routine · ${allRows.filter((r) => r.kind === "agent").length} agent`, onClick: () => onView("all") },
            { key: "calls", tone: "muted", icon: ArrowDownToLine, label: "Calls", value: c.calls, sub: "routine triggers only" },
            { key: "failing", tone: c.failing > 0 ? "bad" : "muted", icon: AlertTriangle, label: "Failing", value: c.failing, sub: "last call failed", onClick: () => onView("failing") },
            { key: "off", tone: "muted", icon: Circle, label: "Turned off", value: c.off, onClick: () => onView("off") },
          ]}
        />
      )}
      {error && allRows.length > 0 && <PageError title="Some triggers may be out of date" onRetry={onRetry} detail={error} />}
      {body}
    </div>
  )
}

export const TRIGGER_PANES = ["overview", "mapping", "calls", "settings"] as const
export type TriggerPane = (typeof TRIGGER_PANES)[number]

export function TriggerPage({
  workspaceId,
  row,
  data,
  pane,
  onPane,
  canEdit,
  onBack,
  onDeleted,
}: {
  workspaceId: string
  row: TriggerRow | null
  data: IncomingData
  pane: TriggerPane
  onPane: (p: TriggerPane) => void
  canEdit: boolean
  onBack: () => void
  onDeleted: () => void
}) {
  const [reveal, setReveal] = React.useState<Reveal | null>(null)
  const [confirm, setConfirm] = React.useState<"rotate" | "delete" | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [rate, setRate] = React.useState<string>("")
  React.useEffect(() => setRate(row?.endpoint?.routine ? String(row.endpoint.routine.rate_limit_per_min) : ""), [row])

  if (!row || !row.endpoint) {
    return data.loading ? <PageLoading rows={4} /> : <PageNotFound what="trigger" back={{ label: "Back to Triggers", onClick: onBack }} />
  }
  const e = row.endpoint
  const hook = e.routine
  const routineDeleted = row.kind === "routine" && !data.targets.some((t) => t.kind === "routine" && t.id === row.target.id)
  const routines = data.targets.filter((t) => t.kind === "routine")
  const url = hook ? null : absolute(e.path)

  const save = async (body: Parameters<IncomingData["hooks"]["update"]>[1], done: string) => {
    if (!hook) return
    setBusy(true)
    try {
      await data.hooks.update(hook.id, body)
      toast.success(done)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "The change was not saved")
    } finally {
      setBusy(false)
    }
  }

  const tabs = [{ id: "overview", label: "Overview" }, ...(hook ? [{ id: "mapping", label: "Mapping" }] : []), { id: "calls", label: "Calls" }, { id: "settings", label: "Settings" }]
  const current = pane === "mapping" && !hook ? "overview" : pane
  const mapping = Object.entries(hook?.inputs_template ?? {})

  let body: React.ReactNode
  if (current === "mapping" && hook) {
    body = <TriggerMapping key={hook.id} hook={hook} targetName={row.target.name} canEdit={canEdit} onSave={(t) => data.hooks.update(hook.id, { inputs_template: t })} />
  } else if (current === "calls") {
    body = <RecentReceipts workspaceId={workspaceId} target={row.target} rows={[e]} />
  } else if (current === "settings") {
    body = (
      <>
        <SettingsCard title="Where it goes" description="The URL stays the same when you change this" icon={ArrowDownToLine}>
          <SettingsRow label="Starts" description={hook ? "Another routine; the URL stays the same" : undefined}>
            {hook ? (
              <select
                aria-label="Starts"
                className={nativeSelect}
                value={hook.target_pipeline_id}
                disabled={!canEdit || busy}
                onChange={(ev) => void save({ target_pipeline_id: ev.target.value }, "Trigger now starts another routine")}
              >
                {routines.every((t) => t.id !== hook.target_pipeline_id) && <option value={hook.target_pipeline_id}>{row.target.name}</option>}
                {routines.map((t) => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </select>
            ) : (
              <span className="text-label text-muted-foreground">{KIND_LABEL[row.kind].one} · {row.target.name}</span>
            )}
          </SettingsRow>
          {hook && (
            <SettingsRow label="Version" description="A pinned version keeps a routine edit from changing what the call does">
              <span className="text-label text-muted-foreground">{hook.target_pipeline_version ? `v${hook.target_pipeline_version} (pinned)` : "Latest"}</span>
            </SettingsRow>
          )}
        </SettingsCard>
        <SettingsCard title="Sender and limits" icon={Shield}>
          <SettingsRow label="Checks the sender" description={row.checks === "GitHub signature" ? "X-Hub-Signature-256; only pull_request events" : "HMAC with the shared secret; Idempotency-Key dedup"}>
            <MetaChip>{row.checks}</MetaChip>
          </SettingsRow>
          <SettingsRow label="Secret" description="Rotate it, then paste the new one at the sender">
            <Button variant="outline" size="sm" disabled={!canEdit} onClick={() => setConfirm("rotate")}>
              <RotateCw className="size-3.5" aria-hidden />
              Rotate…
            </Button>
          </SettingsRow>
          {hook && (
            <>
              <SettingsRow label="Calls per minute" description="More are refused with 429; the sender retries">
                <Input aria-label="Calls per minute" type="number" min={1} className="h-8 w-24 text-control" value={rate} disabled={!canEdit || busy} onChange={(ev) => setRate(ev.target.value)} />
                {Number(rate) !== hook.rate_limit_per_min && Number(rate) > 0 && (
                  <Button size="sm" disabled={busy} onClick={() => void save({ rate_limit_per_min: Number(rate) }, "Limit saved")}>Save</Button>
                )}
              </SettingsRow>
              <SettingsRow label="On">
                <Switch checked={hook.enabled} aria-label="On" disabled={!canEdit || busy} onCheckedChange={(on) => void save({ enabled: on }, on ? "Trigger is on" : "Trigger is off")} />
              </SettingsRow>
            </>
          )}
        </SettingsCard>
        <SettingsCard title="Command line" icon={Terminal}>
          <CliLines
            lines={
              hook
                ? [
                    [`crewship routine webhooks update ${hook.id} --target <routine>`, "Point it at another routine"],
                    [`crewship routine webhooks url ${hook.id}`, "Show the URL"],
                    [`crewship routine webhooks receipts list --webhook ${hook.id}`, "Recent calls"],
                  ]
                : [[`crewship agent rotate-webhook-secret ${row.target.slug}`, "New secret"]]
            }
          />
        </SettingsCard>
        {hook && canEdit && (
          <SettingsDangerCard title="Danger zone">
            <SettingsRow label="Delete trigger" description="The URL stops working; the sender gets 404">
              <Button variant="outline" size="sm" onClick={() => setConfirm("delete")}>
                <Trash2 className="size-3.5" aria-hidden />
                Delete…
              </Button>
            </SettingsRow>
          </SettingsDangerCard>
        )}
      </>
    )
  } else {
    body = (
      <>
        <FlowSteps
          label="What this trigger does"
          steps={[
            { icon: <Globe className="size-[18px]" />, label: "Sender", value: row.checks === "GitHub signature" ? "GitHub" : "Any system with the secret" },
            { icon: <Shield className="size-[18px]" />, label: "Checks", value: hook ? `${row.checks} · ${hook.rate_limit_per_min}/min` : `${row.checks} · 60/min` },
            { icon: <Repeat className="size-[18px]" />, label: "Becomes", value: hook ? `${mapping.length} inputs, plus event, raw and headers` : "The task of a background run" },
            { icon: <IncomingTargetAvatar target={row.target} />, label: "Starts", value: row.target.name },
          ]}
        />
        {routineDeleted && <Banner tone="warn" title="Starts a routine that was deleted" action={<Button size="sm" variant="outline" onClick={() => onPane("settings")}>Settings</Button>}>Point it at another routine, or delete it.</Banner>}
        {row.failing && <Banner tone="bad" title="The last call failed" action={<Button size="sm" variant="outline" onClick={() => onPane("calls")}>Open calls</Button>}>The sender retries; see what happened in Calls.</Banner>}
        <div className="grid gap-3.5 lg:grid-cols-2">
          <InfoCard title="URL" icon={Link2}>
            <div className="flex flex-wrap items-center gap-2 px-4 py-3">
              <span className="min-w-0 flex-1 break-all font-mono text-label text-foreground">{url ?? "Shown once, when the trigger was created"}</span>
              {url && (
                <Button variant="outline" size="sm" onClick={() => void copy(url)}>
                  <Copy className="size-3.5" aria-hidden />
                  Copy
                </Button>
              )}
            </div>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 border-t border-border px-4 py-3 text-label">
              <dt className="text-muted-foreground">Secret</dt>
              <dd className="text-foreground">{e.signed ? "Set · shown once at creation" : "Not set"}</dd>
              <dt className="text-muted-foreground">Accepts</dt>
              <dd className="text-foreground">POST · JSON</dd>
              <dt className="text-muted-foreground">Answers</dt>
              <dd className="text-foreground">202 when accepted; the work runs after</dd>
            </dl>
          </InfoCard>
          <RecentReceipts workspaceId={workspaceId} target={row.target} rows={[e]} />
        </div>
      </>
    )
  }

  return (
    <>
      <ObjectPage
        backLabel="Triggers"
        onBack={onBack}
        lead={<GlyphTile icon={ArrowDownToLine} size={52} />}
        title={row.name}
        titleMono
        description={<>Starts {KIND_LABEL[row.kind].one.toLowerCase()} <b className="font-semibold text-foreground">{row.target.name}</b></>}
        chips={
          <>
            <TriggerState row={row} />
            <MetaChip icon={Shield}>{row.checks}</MetaChip>
            {hook && <MetaChip>{hook.rate_limit_per_min} calls / min</MetaChip>}
          </>
        }
        tabs={tabs}
        tab={current}
        onTab={(t) => onPane(t as TriggerPane)}
      >
        {body}
      </ObjectPage>
      <ConfirmDialog
        open={confirm === "rotate"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Rotate secret? The old one stops now."
        consequences={[
          { tone: "lost", text: "Senders that still use the old secret are refused from now on" },
          { tone: "kept", text: "The URL stays the same" },
        ]}
        confirmLabel="Rotate secret"
        destructive
        onConfirm={async () => {
          if (hook) {
            const result = await data.hooks.update(hook.id, { rotate_secret: true })
            if (result?.signing_secret) setReveal({ secret: result.signing_secret, target: row.target })
          } else {
            const result = await incomingJSON<{ webhook_secret: string }>(`/api/v1/agents/${row.target.id}/webhook-secret/rotate?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
            setReveal({ secret: result.webhook_secret, url: absolute(e.path), target: row.target })
            data.refresh()
          }
        }}
      />
      <ConfirmDialog
        open={confirm === "delete"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`Delete ${row.name}?`}
        consequences={[
          { tone: "lost", text: "The URL stops working; the sender gets 404" },
          { tone: "kept", text: "Runs it already started and their receipts remain" },
        ]}
        confirmLabel="Delete trigger"
        destructive
        onConfirm={async () => {
          if (!hook) return
          await data.hooks.remove(hook.id)
          toast.success("Trigger deleted")
          onDeleted()
        }}
      />
      {reveal && <RevealDialog reveal={reveal} onClose={() => setReveal(null)} />}
    </>
  )
}

/**
 * Mapping (routine triggers): routine input ← what it takes from the call.
 * A draft on the page's Save bar; one Save sends PATCH inputs_template.
 * Testing a mapping against a sample call is not drawn: the server has no
 * dry run for it yet.
 */
function TriggerMapping({
  hook,
  targetName,
  canEdit,
  onSave,
}: {
  hook: PipelineWebhook
  targetName: string
  canEdit: boolean
  onSave: (template: Record<string, unknown>) => Promise<PipelineWebhook | null>
}) {
  const saved = hook.inputs_template ?? {}
  const [rows, setRows] = React.useState<MappingRow[]>(() => mappingRows(saved))
  const draft = mappingTemplate(rows)
  const problem = mappingProblem(rows)
  const keys = new Set([...Object.keys(saved), ...Object.keys(draft)])
  const count = sameMapping(saved, draft) ? 0 : [...keys].filter((k) => JSON.stringify(saved[k]) !== JSON.stringify(draft[k])).length || 1
  return (
    <>
      <InfoCard title="From the call to the routine's inputs" icon={Repeat}>
        <div className="grid gap-2 px-4 py-3">
          <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] gap-1.5 text-label font-medium text-muted-foreground max-sm:hidden" aria-hidden>
            <span>Routine input</span>
            <span>Takes</span>
            <span className="w-7" />
          </div>
          {rows.length === 0 && (
            <p className="text-body text-muted-foreground">
              No mapping. The routine gets the call as <span className="font-mono">event</span>, <span className="font-mono">raw</span> and <span className="font-mono">headers</span>.
            </p>
          )}
          <MappingEditor rows={rows} onChange={setRows} readOnly={!canEdit} problem={problem} />
        </div>
        <p className="border-t border-border px-4 py-2.5 text-label text-muted-foreground">
          <span className="font-mono">event</span>, <span className="font-mono">raw</span> and <span className="font-mono">headers</span> are always there and cannot be overwritten. Agents get them fenced as untrusted text. Inputs come from routine {targetName}; a missing required one rejects the call with 422.
        </p>
      </InfoCard>
      <SettingsSaveBar
        label="Mapping"
        count={canEdit ? count : 0}
        canSave={problem === null}
        onSave={async () => {
          const result = await onSave(draft)
          setRows(mappingRows(result?.inputs_template ?? draft))
        }}
        onDiscard={() => setRows(mappingRows(saved))}
      />
    </>
  )
}
