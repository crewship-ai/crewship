"use client"

import * as React from "react"
import { AlertTriangle } from "lucide-react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { SettingsCard } from "@/components/features/settings/shared"
import { saveInstanceGovernance, type GovSaveResult, type GovTargets, type InstanceGovRow, type InstanceGovSettings } from "./use-instance-keeper"

/**
 * One form for several workspaces at once. Each field shows the value the
 * selected workspaces share, or "Mixed" with what they hold when they differ;
 * nothing is sent for a field the admin did not touch, so a save changes only
 * what was chosen here. Saving always asks first: the server previews the save
 * (dry_run) and the dialog lists, workspace by workspace, what is overwritten.
 */

export type BulkSection = "workspace-judge" | "watchdog" | "alerts" | "leases"

type Key = keyof InstanceGovSettings
interface Option { value: string | number | boolean; label: string }
interface Field { key: Key; label: string; hint: string; options?: Option[]; text?: boolean; when?: (d: Draft, rows: InstanceGovRow[]) => boolean }
type Draft = Partial<Record<Key, string | number | boolean>>

export const SAMPLE_DEFAULT = 5
const eff = (key: Key, v: unknown) => (key === "behavior_sample_every" && (!v || v === 0) ? SAMPLE_DEFAULT : v)

export const BULK_FIELDS: Record<BulkSection, Field[]> = {
  watchdog: [
    { key: "enabled", label: "Watchdog", hint: "Samples tool calls and raises findings", options: [{ value: true, label: "On" }, { value: false, label: "Off" }] },
    {
      key: "behavior_sample_every", label: "Review", hint: "How many tool calls per review; each review is a judge call",
      options: [1, 2, 5, 10, 20, 50].map((n) => ({ value: n, label: n === 1 ? "Every call" : `1 in ${n}` })),
    },
  ],
  alerts: [
    { key: "deny_notify_min_risk", label: "Alert on DENY from risk", hint: "A denial at or above this risk also reaches the inbox; escalations always do", options: [1, 3, 5, 7, 9, 10].map((n) => ({ value: n, label: String(n) })) },
    { key: "require_second_approver", label: "Four-eyes approval", hint: "A credential escalation needs someone other than the agent's owner", options: [{ value: true, label: "On" }, { value: false, label: "Off" }] },
  ],
  leases: [
    {
      key: "auto_lease_seconds", label: "Approved access lasts", hint: "Standing keeps a grant until revoked",
      options: [{ value: 0, label: "Standing" }, { value: 900, label: "15 min" }, { value: 3600, label: "1 h" }, { value: 28800, label: "8 h" }, { value: 86400, label: "24 h" }],
    },
  ],
  "workspace-judge": [
    {
      key: "gov_model_provider", label: "Judge", hint: "The instance judge, or a different one for these workspaces",
      options: [{ value: "", label: "Instance judge" }, { value: "ollama", label: "Ollama" }, { value: "anthropic", label: "Anthropic" }, { value: "openai_compat", label: "OpenAI-compatible" }],
    },
    {
      key: "gov_model_id", label: "Model", hint: "Required with a provider other than the instance judge", text: true,
      when: (d, rows) => (d.gov_model_provider ?? commonValue(rows, "gov_model_provider")) !== "" ,
    },
  ],
}

const SECTION_TITLE: Record<BulkSection, string> = {
  "workspace-judge": "Judge per workspace", watchdog: "Watchdog", alerts: "Alerts & approvals", leases: "Credential leases",
}

/** The value every row shares, or undefined when they differ. */
export function commonValue(rows: InstanceGovRow[], key: Key): unknown {
  if (rows.length === 0) return undefined
  const first = eff(key, rows[0][key])
  return rows.every((r) => eff(key, r[key]) === first) ? first : undefined
}

function labelOf(field: Field, v: unknown): string {
  if (field.text) return v ? String(v) : "—"
  return field.options?.find((o) => o.value === v)?.label ?? String(v)
}

function Segmented({ field, value, onPick, name }: { field: Field; value: unknown; onPick: (v: Option["value"]) => void; name: string }) {
  return (
    <div role="radiogroup" aria-label={name} className="inline-flex flex-wrap gap-0.5 rounded-lg border border-control-border p-0.5">
      {field.options!.map((o) => (
        <button
          key={String(o.value)}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          onClick={() => onPick(o.value)}
          className={cn(
            "rounded-md px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground coarse:min-h-11 coarse:px-3.5",
            value === o.value && "bg-accent font-medium text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function BulkGovernanceForm({
  section, rows, all, onSaved,
}: {
  section: BulkSection
  /** The selected workspaces. */
  rows: InstanceGovRow[]
  /** Every workspace is selected: the save also sets the instance defaults. */
  all: boolean
  onSaved: (r: GovSaveResult) => void
}) {
  const fields = BULK_FIELDS[section]
  const [draft, setDraft] = React.useState<Draft>({})
  // The preview, and the exact request it previewed: confirming sends that
  // request, never whatever the form holds by then (review R3).
  const [preview, setPreview] = React.useState<{ result: GovSaveResult; targets: GovTargets; set: Partial<InstanceGovSettings> } | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  // A draft belongs to the workspaces it was made for: the section, which
  // workspaces, and whether "all" (which also sets the defaults). Another
  // selection of the same size is another set of workspaces.
  const scopeKey = `${section}|${all ? "all" : ""}|${rows.map((r) => r.workspace_id).sort().join(",")}`
  const scopeRef = React.useRef(scopeKey)
  const draftRef = React.useRef(draft)
  draftRef.current = draft
  React.useEffect(() => {
    if (scopeRef.current === scopeKey) return
    scopeRef.current = scopeKey
    if (Object.keys(draftRef.current).length > 0) toast.info("The selection changed, so the unsaved changes were dropped.")
    setDraft({}); setError(null); setPreview(null); setBusy(false)
  }, [scopeKey])

  const targets: GovTargets = all ? { all: true } : { workspaces: rows.map((r) => r.workspace_id) }
  const set = Object.fromEntries(Object.entries(draft)) as Partial<InstanceGovSettings>
  const dirty = Object.keys(draft).length > 0
  const n = rows.length

  async function review() {
    const asked = scopeKey
    const req = { targets, set }
    setBusy(true); setError(null)
    try {
      const result = await saveInstanceGovernance(req.targets, req.set, true)
      // An answer for a selection that is no longer on screen is dropped.
      if (scopeRef.current !== asked) return
      setPreview({ result, ...req })
    } catch (e) {
      if (scopeRef.current === asked) setError(e instanceof Error ? e.message : "The save could not be checked")
    } finally {
      if (scopeRef.current === asked) setBusy(false)
    }
  }

  async function apply() {
    if (!preview) return
    try {
      const r = await saveInstanceGovernance(preview.targets, preview.set, false, preview.result.preview_id)
      for (const w of r.workspaces) for (const warn of w.warnings ?? []) toast.warning(`${w.workspace_name}: ${warn}`)
      toast.success(`${SECTION_TITLE[section]} saved in ${r.changed} workspace${r.changed === 1 ? "" : "s"}`)
      setDraft({}); setPreview(null)
      onSaved(r)
    } catch (e) {
      setPreview(null)
      setError(e instanceof Error ? e.message : "The save failed; nothing was changed")
    }
  }

  const byField = new Map(fields.map((f) => [f.key as string, f]))

  return (
    <>
      <div role="alert" data-slot="bulk-warning" className="flex items-start gap-2.5 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2.5 text-[12.5px]">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" />
        <p>
          <b className="text-warn">{all ? `All ${n} workspace${n === 1 ? "" : "s"} selected.` : `${n} workspaces selected.`}</b>{" "}
          Saving writes what you change here into {all ? "every workspace" : "each of them"} and replaces the value it has now.
          Fields you leave alone stay as each workspace has them.{all && " New workspaces will start with these settings too."}
        </p>
      </div>
      <SettingsCard title={SECTION_TITLE[section]} description={all ? "Every workspace, and what a new workspace starts with" : `Settings for ${n} workspaces at once`}>
        <div className="flex flex-col gap-3 px-4 py-3">
          {fields.filter((f) => !f.when || f.when(draft, rows)).map((f) => {
            const shared = commonValue(rows, f.key)
            const value = f.key in draft ? draft[f.key] : shared
            const distinct = [...new Set(rows.map((r) => labelOf(f, eff(f.key, r[f.key]))))]
            return (
              <div key={f.key} className="grid gap-2 sm:grid-cols-[11rem_1fr] sm:items-center" data-field={f.key}>
                <div>
                  <div className="text-[13px]">{f.label}</div>
                  <div className="text-[11px] text-muted-foreground">{f.hint}</div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  {f.text ? (
                    <Input aria-label={f.label} className="h-8 max-w-xs text-xs" value={String(value ?? "")}
                      placeholder={shared === undefined ? "Mixed" : undefined}
                      onChange={(e) => setDraft((d) => ({ ...d, [f.key]: e.target.value }))} />
                  ) : (
                    <Segmented field={f} value={value} name={f.label} onPick={(v) => setDraft((d) => ({ ...d, [f.key]: v }))} />
                  )}
                  {shared === undefined && !(f.key in draft) && (
                    <span className="font-mono text-[10.5px] text-warn" data-slot="mixed">MIXED · {distinct.join(" / ")}</span>
                  )}
                  {f.key in draft && (
                    <button type="button" className="text-[11px] text-muted-foreground underline-offset-2 hover:underline"
                      onClick={() => setDraft((d) => { const { [f.key]: _, ...rest } = d; return rest })}>
                      keep as is
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
        <div className="flex flex-wrap items-center gap-2 border-t border-border px-4 py-2.5">
          <Button size="sm" variant="destructive" disabled={!dirty || busy} onClick={() => void review()}>
            {all ? `Overwrite all ${n} workspace${n === 1 ? "" : "s"}…` : `Overwrite ${n} workspaces…`}
          </Button>
          <Button size="sm" variant="outline" disabled={!dirty || busy} onClick={() => setDraft({})}>Reset</Button>
          {!dirty && <span className="text-[12px] text-muted-foreground">Change a value to save</span>}
          {error && <span role="status" className="text-[12px] text-destructive">{error}</span>}
        </div>
      </SettingsCard>

      <ConfirmDialog
        open={preview !== null}
        onOpenChange={(o) => { if (!o) setPreview(null) }}
        title={`Overwrite ${SECTION_TITLE[section]} in ${preview?.result.changed ?? 0} workspace${preview?.result.changed === 1 ? "" : "s"}?`}
        destructive
        confirmLabel={preview?.result.changed ? `Overwrite ${preview.result.changed} workspace${preview.result.changed === 1 ? "" : "s"}` : "Save"}
        description={preview && <OverwriteTable result={preview.result} fields={byField} />}
        consequences={[
          { tone: "lost", text: "Each listed workspace's own value for these fields is replaced." },
          { tone: "kept", text: "Fields you did not change stay as each workspace has them. Every change is in the instance audit log." },
          ...(preview?.result.defaults_updated ? [{ tone: "warn" as const, text: "New workspaces will start with these settings." }] : []),
        ]}
        onConfirm={apply}
      />
    </>
  )
}

function OverwriteTable({ result, fields }: { result: GovSaveResult; fields: Map<string, Field> }) {
  const show = (field: string, v: unknown) => {
    const f = fields.get(field)
    return f ? labelOf(f, eff(field as Key, v)) : String(v)
  }
  const name = (field: string) => fields.get(field)?.label ?? field
  return (
    <div className="mt-2 max-h-64 overflow-auto rounded-md border border-border">
      <table className="w-full text-left text-[12px]" data-slot="overwrite-table">
        <thead className="text-[10.5px] uppercase text-muted-foreground">
          <tr><th className="px-2.5 py-1.5 font-medium">Workspace</th><th className="px-2.5 py-1.5 font-medium">Change</th></tr>
        </thead>
        <tbody>
          {result.workspaces.map((w) => (
            <tr key={w.workspace_id} className={cn("border-t border-border", w.changes.length === 0 && "text-muted-foreground")}>
              <td className="px-2.5 py-1.5">{w.workspace_name}</td>
              <td className="px-2.5 py-1.5">
                {w.changes.length === 0 ? "no change" : (
                  <ul className="space-y-0.5">
                    {w.changes.map((c) => <li key={c.field}>{`${name(c.field)}: ${show(c.field, c.before)} → ${show(c.field, c.after)}`}</li>)}
                  </ul>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
