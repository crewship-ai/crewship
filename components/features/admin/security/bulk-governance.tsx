"use client"

import * as React from "react"
import { AlertTriangle } from "lucide-react"
import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { toastSaveError } from "@/components/ui/page-save-bar"
import { SettingsCard, SettingsSaveBar, controlHeight, settingsTable, settingsTh, settingsTd } from "@/components/features/settings/shared"
import { saveDefaults, saveInstanceGovernance, type DefaultsResult, type GovSaveResult, type GovTargets, type InstanceGovRow, type InstanceGovSettings } from "./use-instance-keeper"
import { PRESET_KEYS, PRESET_LABEL } from "./security-model"
import { SAMPLE_DEFAULT } from "./watch-labels"

/**
 * One form for several workspaces at once. Each field shows the value the
 * selected workspaces share, or "Mixed" with what they hold when they differ;
 * nothing is sent for a field the admin did not touch, so a save changes only
 * what was chosen here. The edits join the page's Save bar, and Save always
 * asks first: the server previews the save (dry_run) and the dialog lists,
 * workspace by workspace, what is overwritten. A failure is a corner toast.
 */

export type BulkSection = "workspace-judge" | "watchdog" | "alerts" | "leases"

type Key = keyof InstanceGovSettings
interface Option { value: string | number | boolean; label: string }
/** A number beside the quick picks, for any other valid value: `scale` turns what is typed into what is stored (minutes into seconds). */
interface NumberBeside { min: number; max: number; unit: string; scale?: number }
interface Field {
  key: Key; label: string; hint: string; options?: Option[]; text?: boolean
  /** Any number in range, beside the quick picks. */
  number?: NumberBeside
  /** The watch presets: toggles, held as a sorted comma list like the panel's. */
  presets?: boolean
  when?: (d: Draft, rows: InstanceGovRow[]) => boolean
}
type Draft = Partial<Record<Key, string | number | boolean>>

const presetKey = (v: unknown) => (Array.isArray(v) ? [...(v as string[])].sort().join(",") : "")
const eff = (key: Key, v: unknown) => key === "watch_presets" ? presetKey(v) : (key === "behavior_sample_every" && (!v || v === 0) ? SAMPLE_DEFAULT : v)

export const BULK_FIELDS: Record<BulkSection, Field[]> = {
  watchdog: [
    { key: "enabled", label: "Watchdog", hint: "Samples tool calls and raises findings", options: [{ value: true, label: "On" }, { value: false, label: "Off" }] },
    {
      key: "behavior_sample_every", label: "Check", hint: "How many tool calls per check; each check is a judge call. Any number from 1 to 100.",
      options: [1, 2, 5, 10, 20, 50].map((n) => ({ value: n, label: n === 1 ? "Every call" : `1 in ${n}` })),
      number: { min: 1, max: 100, unit: "1 in" },
    },
    { key: "watch_presets", label: "Flag", hint: "What counts as suspicious; replaces the flags each workspace has", presets: true },
  ],
  alerts: [
    {
      key: "deny_notify_min_risk", label: "Alert on a denial from risk", hint: "A denied request at or above this risk raises an alert; escalations always do. Any number from 1 to 10.",
      options: [1, 3, 5, 7, 9, 10].map((n) => ({ value: n, label: String(n) })), number: { min: 1, max: 10, unit: "risk" },
    },
    { key: "require_second_approver", label: "Second approver", hint: "Someone other than the owner of the requesting agent confirms an approval", options: [{ value: true, label: "On" }, { value: false, label: "Off" }] },
  ],
  leases: [
    {
      key: "auto_lease_seconds", label: "Approved access lasts", hint: "Applies to level 3 and 4 credentials. Until revoked keeps access until someone revokes it.",
      options: [{ value: 0, label: "Until revoked" }, { value: 900, label: "15 min" }, { value: 3600, label: "1 h" }, { value: 28800, label: "8 h" }, { value: 86400, label: "24 h" }],
      number: { min: 1, max: 43200, unit: "min", scale: 60 },
    },
  ],
  "workspace-judge": [
    {
      key: "gov_model_provider", label: "Judge", hint: "The instance judge, or its own judge for these workspaces",
      options: [{ value: "", label: "Instance judge" }, { value: "ollama", label: "Ollama" }, { value: "anthropic", label: "Anthropic" }, { value: "openai_compat", label: "OpenAI-compatible" }],
    },
    {
      key: "gov_model_id", label: "Model", hint: "Required with its own judge. The key is set on one workspace at a time.", text: true,
      when: (d, rows) => (d.gov_model_provider ?? commonValue(rows, "gov_model_provider")) !== "" ,
    },
  ],
}

export const SECTION_TITLE: Record<BulkSection, string> = {
  "workspace-judge": "Judge", watchdog: "Watchdog", alerts: "Alerts and approval", leases: "Access time",
}

/** The value every row shares, or undefined when they differ. */
export function commonValue(rows: InstanceGovRow[], key: Key): unknown {
  if (rows.length === 0) return undefined
  const first = eff(key, rows[0][key])
  return rows.every((r) => eff(key, r[key]) === first) ? first : undefined
}

function labelOf(field: Field, v: unknown): string {
  if (field.presets) {
    const keys = presetKey(v) === "" ? [] : presetKey(v).split(",")
    return keys.length ? keys.map((k) => PRESET_LABEL[k] ?? k).join(", ") : "None"
  }
  if (field.text) return v ? String(v) : "—"
  if (field.number && !field.options?.some((o) => o.value === v) && typeof v === "number") {
    return field.number.scale ? `${v / field.number.scale} ${field.number.unit}` : `${field.number.unit} ${v}`.replace(/^risk /, "")
  }
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
            "rounded-md px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground coarse:min-h-[44px] coarse:px-3.5",
            value === o.value && "bg-accent font-medium text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/**
 * Any other valid value beside the quick picks. Typing a number that is not
 * valid sets nothing (and says why); clearing the box drops the pick.
 */
function NumberField({ field, value, onSet, onClear }: { field: Field; value: unknown; onSet: (v: number) => void; onClear: () => void }) {
  const n = field.number!
  const scale = n.scale ?? 1
  const shown = typeof value === "number" && !field.options?.some((o) => o.value === value) ? String(value / scale) : ""
  const [text, setText] = React.useState(shown)
  // A quick pick moves the value away from what was typed: the box follows.
  React.useEffect(() => { setText(shown) }, [shown])
  const num = Number(text)
  const bad = text.trim() !== "" && (!Number.isInteger(num) || num < n.min || num > n.max)
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="text-label text-muted-foreground max-sm:hidden">or</span>
      <Input type="number" min={n.min} max={n.max} step={1} inputMode="numeric" value={text} aria-label={`${field.label}: any number from ${n.min} to ${n.max}`}
        aria-invalid={bad} className={cn(controlHeight, "w-20 text-right tabular-nums")} data-testid={`bulk-number-${field.key}`}
        onChange={(e) => {
          const v = e.target.value
          setText(v)
          const k = Number(v)
          if (v.trim() === "") onClear()
          else if (Number.isInteger(k) && k >= n.min && k <= n.max) onSet(k * scale)
        }} />
      <span className="text-label text-muted-foreground">{n.unit}</span>
      {bad && <span className="text-label text-destructive" data-testid={`bulk-number-invalid-${field.key}`}>{n.min} to {n.max}</span>}
    </span>
  )
}

/** The watch presets as toggles; the value is a sorted comma list. */
function PresetToggles({ value, onPick, name }: { value: string; onPick: (v: string) => void; name: string }) {
  const on = value === "" ? [] : value.split(",")
  return (
    <div role="group" aria-label={name} className="flex flex-wrap gap-1.5">
      {PRESET_KEYS.map((k) => {
        const checked = on.includes(k)
        return (
          <button key={k} type="button" role="checkbox" aria-checked={checked}
            onClick={() => onPick([...(checked ? on.filter((x) => x !== k) : [...on, k])].sort().join(","))}
            className={cn("inline-flex h-8 items-center rounded-lg border px-2.5 text-xs transition-colors coarse:min-h-[44px]",
              checked ? "border-primary bg-primary/10 text-foreground" : "border-border text-muted-foreground hover:text-foreground")}>
            {PRESET_LABEL[k]}
          </button>
        )
      })}
    </div>
  )
}

/** The draft as the server's partial update: the flags travel as a list. */
function draftToSet(draft: Draft): Partial<InstanceGovSettings> {
  const { watch_presets, ...rest } = draft
  return {
    ...(rest as Partial<InstanceGovSettings>),
    ...(watch_presets === undefined ? {} : { watch_presets: String(watch_presets) === "" ? [] : String(watch_presets).split(",") }),
  }
}

/** The control for one field: quick picks (and any number), the flags, or text. */
function FieldControl({ f, value, mixed, onValue, onClear }: {
  f: Field; value: unknown; mixed?: boolean; onValue: (v: string | number | boolean) => void; onClear: () => void
}) {
  if (f.presets) return <PresetToggles value={presetKey(value)} name={f.label} onPick={onValue} />
  if (f.text) {
    return (
      <Input aria-label={f.label} className={cn(controlHeight, "max-w-xs")} value={String(value ?? "")}
        placeholder={mixed ? "Mixed" : undefined} onChange={(e) => onValue(e.target.value)} />
    )
  }
  return (
    <>
      <Segmented field={f} value={value} name={f.label} onPick={onValue} />
      {f.number && <NumberField field={f} value={value} onSet={onValue} onClear={onClear} />}
    </>
  )
}

export function BulkGovernanceForm({
  section, rows, all, onSaved, onEditOne, inDialog,
}: {
  section: BulkSection
  /** The selected workspaces. */
  rows: InstanceGovRow[]
  /** "All workspaces" is the scope: every existing workspace. It never
   *  touches the defaults for new ones, which are saved on their own. */
  all: boolean
  onSaved: (r: GovSaveResult) => void
  /** Open one workspace's full editor: the settings that belong to a single
   *  workspace (contact, judge key, watch rules) are not in this form. */
  onEditOne?: (workspaceId: string) => void
  /** Drawn in the dialog the selection bar opens (Admin › Security ›
   *  Workspaces): the fields and its own Cancel / Review, no card and no page
   *  Save bar. The preview before writing is the same. */
  inDialog?: { onCancel: () => void }
}) {
  const fields = BULK_FIELDS[section]
  const [draft, setDraft] = React.useState<Draft>({})
  // The preview, and the exact request it previewed: confirming sends that
  // request, never whatever the form holds by then (review R3).
  const [preview, setPreview] = React.useState<{ result: GovSaveResult; targets: GovTargets; set: Partial<InstanceGovSettings> } | null>(null)
  const [busy, setBusy] = React.useState(false)

  // A draft belongs to the workspaces it was made for: the section, which
  // workspaces, and whether "all" (which also sets the defaults). Another
  // selection of the same size is another set of workspaces.
  const scopeKey = `${section}|${all ? "all" : ""}|${rows.map((r) => r.workspace_id).sort().join(",")}`
  const scopeRef = React.useRef(scopeKey)
  // Bumped on every change of scope, so a preview asked for before the
  // selection went away and came back is still recognised as stale.
  const epoch = React.useRef(0)
  const draftRef = React.useRef(draft)
  draftRef.current = draft
  React.useEffect(() => {
    if (scopeRef.current === scopeKey) return
    scopeRef.current = scopeKey
    epoch.current += 1
    if (Object.keys(draftRef.current).length > 0) toast.info("The selection changed, so the unsaved changes were dropped.")
    setDraft({}); setPreview(null); setBusy(false)
  }, [scopeKey])

  const targets: GovTargets = all ? { all: true } : { workspaces: rows.map((r) => r.workspace_id) }
  const set = draftToSet(draft)
  const n = rows.length

  /** The bar's Save: preview first. A failed preview is thrown for the bar's toast. */
  async function review() {
    const asked = epoch.current
    const req = { targets, set }
    setBusy(true)
    try {
      const result = await saveInstanceGovernance(req.targets, req.set, true)
      // An answer for a selection that is no longer on screen is dropped.
      if (epoch.current !== asked) return
      setPreview({ result, ...req })
    } catch (e) {
      if (epoch.current === asked) throw e instanceof Error ? e : new Error("The save could not be checked")
    } finally {
      if (epoch.current === asked) setBusy(false)
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
      toastSaveError(SECTION_TITLE[section], e instanceof Error ? e.message : "The save failed; nothing was changed.")
    }
  }

  const byField = new Map(fields.map((f) => [f.key as string, f]))

  const fieldRows = fields.filter((f) => !f.when || f.when(draft, rows)).map((f) => {
    const shared = commonValue(rows, f.key)
    const value = f.key in draft ? draft[f.key] : shared
    const distinct = [...new Set(rows.map((r) => labelOf(f, eff(f.key, r[f.key]))))]
    return (
      <div key={f.key} className={cn("grid gap-2", !inDialog && "sm:grid-cols-[11rem_1fr] sm:items-center")} data-field={f.key}>
        <div>
          <div className="text-control">{f.label}</div>
          <div className="text-label text-muted-foreground">{f.hint}</div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <FieldControl f={f} value={value} mixed={shared === undefined}
            onValue={(v) => setDraft((d) => ({ ...d, [f.key]: v }))}
            onClear={() => setDraft((d) => { const { [f.key]: _, ...rest } = d; return rest })} />
          {shared === undefined && !(f.key in draft) && (
            <span className="font-mono text-micro text-warn" data-slot="mixed">MIXED · {distinct.join(" / ")}</span>
          )}
          {f.key in draft && (
            <button type="button" className="text-label text-muted-foreground underline-offset-2 hover:underline"
              onClick={() => setDraft((d) => { const { [f.key]: _, ...rest } = d; return rest })}>
              keep as is
            </button>
          )}
        </div>
      </div>
    )
  })
  const confirm = (
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
        { tone: "kept", text: "New workspaces are not changed; they start from what New workspaces holds." },
      ]}
      onConfirm={apply}
    />
  )

  if (inDialog) {
    const edits = Object.keys(draft).length
    return (
      <div className="flex flex-col gap-4" data-slot="bulk-dialog-form">
        <div className="flex flex-col gap-3">{fieldRows}</div>
        <p className="text-label text-muted-foreground">
          Only what you set here changes; the rest stays as each workspace has it. Saving replaces the value in each of the {n} workspaces. New workspaces are not changed.
        </p>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" size="sm" onClick={inDialog.onCancel}>Cancel</Button>
          <Button type="button" size="sm" disabled={edits === 0 || busy}
            onClick={() => { review().catch((e: Error) => toastSaveError(SECTION_TITLE[section], e.message)) }}>
            Review {edits === 0 ? "changes" : `${edits} change${edits === 1 ? "" : "s"}`}
          </Button>
        </div>
        {confirm}
      </div>
    )
  }

  return (
    <>
      <div role="alert" data-slot="bulk-warning" className="flex items-start gap-2.5 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2.5 text-label">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" />
        <p>
          <b className="text-warn">{all ? `All ${n} workspace${n === 1 ? "" : "s"} selected.` : `${n} workspaces selected.`}</b>{" "}
          Saving writes what you change here into {all ? "every existing workspace" : "each of them"} and replaces the value it has now.
          Fields you leave alone stay as each workspace has them. New workspaces are not changed: they start from the New workspaces settings.
        </p>
      </div>
      {onEditOne && (
        <p className="text-label text-muted-foreground" data-slot="edit-one">
          The security contact, a workspace&apos;s own judge key and its own rules are set one workspace at a time:{" "}
          {rows.map((r, i) => (
            <React.Fragment key={r.workspace_id}>
              {i > 0 && " · "}
              <button type="button" onClick={() => onEditOne(r.workspace_id)} className="text-primary-hover underline-offset-2 hover:underline coarse:min-h-[44px]"
                aria-label={`Edit ${r.workspace_name} on its own`}>
                {r.workspace_name}
              </button>
            </React.Fragment>
          ))}
        </p>
      )}
      <SettingsCard title={SECTION_TITLE[section]} description={all ? "Every existing workspace at once" : `Settings for ${n} workspaces at once`}>
        <div className="flex flex-col gap-3 px-4 py-3">
          {fieldRows}
        </div>
      </SettingsCard>
      <SettingsSaveBar label={SECTION_TITLE[section]} count={Object.keys(draft).length} saving={busy}
        onSave={review} onDiscard={() => setDraft({})} />

      {confirm}
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
      <table className={settingsTable} data-slot="overwrite-table">
        <thead>
          <tr><th className={settingsTh}>Workspace</th><th className={settingsTh}>Change</th></tr>
        </thead>
        <tbody>
          {result.workspaces.map((w) => (
            <tr key={w.workspace_id} className={cn(w.changes.length === 0 && "text-muted-foreground")}>
              <td className={settingsTd}>{w.workspace_name}</td>
              <td className={settingsTd}>
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

/** Every field the defaults carry, from all the per-workspace sections. */
const DEFAULT_FIELDS: Field[] = (["watchdog", "alerts", "leases", "workspace-judge"] as BulkSection[]).flatMap((k) => BULK_FIELDS[k])

/**
 * Defaults for new workspaces: the template a workspace copies when it is
 * created. An operation of its own — its own preview and confirmation — and
 * it changes no existing workspace; "All workspaces" on a setting's page does
 * that.
 */
export function DefaultsForm({ current, onSaved }: { current: InstanceGovSettings; onSaved: () => void }) {
  const [draft, setDraft] = React.useState<Draft>({})
  const [preview, setPreview] = React.useState<{ result: DefaultsResult; set: Partial<InstanceGovSettings> } | null>(null)
  const [busy, setBusy] = React.useState(false)
  const row = { ...current, workspace_id: "defaults", workspace_name: "New workspaces", workspace_slug: "defaults", configured: true } as InstanceGovRow
  const set = draftToSet(draft)
  const byField = new Map(DEFAULT_FIELDS.map((f) => [f.key as string, f]))

  /** The bar's Save: preview first. A failed preview is thrown for the bar's toast. */
  async function review() {
    setBusy(true)
    try {
      setPreview({ result: await saveDefaults(set, true), set })
    } catch (e) {
      throw e instanceof Error ? e : new Error("The change could not be checked")
    } finally {
      setBusy(false)
    }
  }
  async function apply() {
    if (!preview) return
    try {
      await saveDefaults(preview.set, false, preview.result.preview_id)
      toast.success("New workspaces saved")
      setDraft({}); setPreview(null)
      onSaved()
    } catch (e) {
      setPreview(null)
      toastSaveError("New workspaces", e instanceof Error ? e.message : "The save failed; nothing was changed.")
    }
  }

  return (
    <>
      <SettingsCard title="Starts with" description="A new workspace copies these when it is created. No existing workspace changes.">
        <div className="flex flex-col gap-3 px-4 py-3">
          {DEFAULT_FIELDS.filter((f) => !f.when || f.when(draft, [row])).map((f) => {
            const value = f.key in draft ? draft[f.key] : eff(f.key, row[f.key])
            return (
              <div key={f.key} className="grid gap-2 sm:grid-cols-[11rem_1fr] sm:items-center" data-field={f.key}>
                <div>
                  <div className="text-control">{f.label}</div>
                  <div className="text-label text-muted-foreground">{f.hint}</div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <FieldControl f={f} value={value} onValue={(v) => setDraft((d) => ({ ...d, [f.key]: v }))}
                    onClear={() => setDraft((d) => { const { [f.key]: _, ...rest } = d; return rest })} />
                </div>
              </div>
            )
          })}
        </div>
      </SettingsCard>
      <SettingsSaveBar label="New workspaces" count={Object.keys(draft).length} saving={busy}
        onSave={review} onDiscard={() => setDraft({})} />
      <ConfirmDialog
        open={preview !== null}
        onOpenChange={(o) => { if (!o) setPreview(null) }}
        title="Save what new workspaces start with?"
        confirmLabel="Save defaults"
        description={preview && (
          <ul className="mt-2 space-y-0.5 rounded-md border border-border px-3 py-2 text-label" data-slot="defaults-changes">
            {preview.result.changes.map((c) => {
              const f = byField.get(c.field)
              const show = (v: unknown) => (f ? labelOf(f, eff(c.field as Key, v)) : String(v))
              return <li key={c.field}>{`${f?.label ?? c.field}: ${show(c.before)} → ${show(c.after)}`}</li>
            })}
          </ul>
        )}
        consequences={[
          { tone: "kept", text: "No existing workspace changes. To change them, tick workspaces in the Workspaces view." },
          { tone: "warn", text: "Every workspace created from now on starts with these settings." },
        ]}
        onConfirm={apply}
      />
    </>
  )
}

