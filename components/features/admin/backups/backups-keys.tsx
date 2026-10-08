"use client"

import * as React from "react"
import { toast } from "sonner"
import { Bell, BellRing, KeyRound, LockKeyhole } from "lucide-react"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsRow, SettingsSaveBar, SettingsSummary, SummaryItem, settingsControl, controlHeight } from "@/components/features/settings/shared"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Input } from "@/components/ui/input"
import { StatusPill } from "@/components/ui/status-pill"
import { Gate } from "./backups-kit"
import { InfoTip, InstanceBadge } from "./backups-storage"
import {
  formatWhen, shortKey, type AlertChannel, type AlertDelivery, type AlertEvents, type BackupIncident, type BackupRecipient, type BackupSettings, type VaultKeysResponse,
} from "./backups-model"
import {
  RECOVERY_SHEET_HREF, addRecipient, removeRecipient, saveBackupSettings, sendTestAlert, setRecoveryKit,
  useBackupSettings, useIncidents, useRecipients, useVaultKeys,
} from "./use-backup-settings"
import { perform, performSave } from "./use-backups-data"
import type { SectionCtx } from "./backups-console"

/**
 * Backups › Keys & alerts (instance setting): the AGE keys a backup is
 * encrypted to (public halves only), the vault keys that open the secrets
 * inside restored data, and who hears — and when — that a backup is wrong.
 */
export function BackupsKeys({ ctx }: { ctx: SectionCtx }) {
  const recipients = useRecipients()
  const vault = useVaultKeys()
  const settings = useBackupSettings()
  const incidents = useIncidents()
  const keys = recipients.data?.length
  const kit = vault.data?.recovery_kit
  const channels = settings.data ? (settings.data.channels ?? []).length : undefined
  return (
    <>
      <SettingsSummary>
        <InstanceBadge />
        {keys !== undefined && (keys ? <SummaryItem n={keys}>backup key{keys === 1 ? "" : "s"}</SummaryItem> : <SummaryItem tone="danger">no backup key</SummaryItem>)}
        {kit && (!kit.available
          ? <SummaryItem tone="warn">recovery kit missing a key</SummaryItem>
          : <SummaryItem>recovery kit {kit.enabled ? "on" : "off"}</SummaryItem>)}
        {channels !== undefined && <SummaryItem n={channels}>alert channel{channels === 1 ? "" : "s"}</SummaryItem>}
      </SettingsSummary>
      <SettingsCard icon={KeyRound} tint="var(--info)" title="Backup keys (AGE)" description="Public keys; the private halves stay off this server"
        actions={<Button asChild size="sm" variant="outline" className={rowButton}><a href={RECOVERY_SHEET_HREF} download="crewship-recovery-sheet.md">Recovery sheet</a></Button>}>
        <Gate resource={recipients} what="Backup keys" skeleton="h-[96px]">
          {(list) => <RecipientsList list={list} ctx={ctx} reload={recipients.reload} />}
        </Gate>
      </SettingsCard>
      <SettingsCard icon={LockKeyhole} tint="var(--destructive)" title="Vault keys" description="Open the secrets inside restored data">
        <Gate resource={vault} what="Vault keys" skeleton="h-[96px]">
          {(v) => <VaultList vault={v} ctx={ctx} reload={() => vault.reload()} />}
        </Gate>
      </SettingsCard>
      <Gate resource={settings} what="Alert settings" skeleton="h-[200px]">
        {(s) => <AlertsBody settings={s} incident={incidents.data?.find((i) => i.state === "open") ?? null} ctx={ctx} reload={settings.reload} />}
      </Gate>
    </>
  )
}

const rowButton = "h-7 px-2.5 text-xs coarse:h-[2.75rem]"

export function RecipientsList({ list, ctx, reload }: { list: BackupRecipient[]; ctx: SectionCtx; reload?: () => void }) {
  const [removing, setRemoving] = React.useState<BackupRecipient | null>(null)
  const [adding, setAdding] = React.useState(false)
  const [form, setForm] = React.useState({ name: "", public_key: "", holder: "" })
  const valid = form.name.trim() && /^age1[0-9a-z]{20,}$/.test(form.public_key.trim())
  return (
    <>
      {list.length === 0 && (
        <SettingsRow label={<span className="text-warn">No backup key yet</span>} description="Every backup is encrypted: add one before the first run">
          <StatusPill tone="warn" label="needed" />
        </SettingsRow>
      )}
      {list.map((r) => (
        <SettingsRow key={r.id} label={`${r.name} · ${shortKey(r.public_key)}`}
          description={[r.holder, r.used_by?.length ? `used by ${r.used_by.join(", ")}` : ""].filter(Boolean).join(" · ") || undefined}>
          <Button type="button" size="sm" variant="outline" className={rowButton} onClick={() => setRemoving(r)} disabled={list.length === 1 || !!r.used_by?.length}
            title={list.length === 1 ? "The last key cannot go: every backup is encrypted" : r.used_by?.length ? `Used by ${r.used_by.join(", ")}: change the plan first` : undefined}>Remove</Button>
        </SettingsRow>
      ))}
      {adding ? (
        <div className="flex flex-col gap-2 border-t border-border px-4 py-3 text-[13px]">
          <Input aria-label="Key name" placeholder="Name, e.g. ops-2027" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })}
            className={controlHeight} />
          <Input aria-label="Public key" placeholder="age1…" value={form.public_key} onChange={(e) => setForm({ ...form, public_key: e.target.value })} spellCheck={false}
            className={cn(controlHeight, "font-mono")} />
          <Input aria-label="Who holds the private key" placeholder="Who holds the private key" value={form.holder} onChange={(e) => setForm({ ...form, holder: e.target.value })}
            className={controlHeight} />
          <div className="flex gap-1.5">
            <Button type="button" size="sm" className={rowButton} disabled={!valid} onClick={async () => {
              const out = await perform(ctx.demo, () => addRecipient({ name: form.name.trim(), public_key: form.public_key.trim(), holder: form.holder.trim() }), "Key added · new backups are encrypted to it", "The key could not be added")
              if (out) { setAdding(false); setForm({ name: "", public_key: "", holder: "" }); reload?.() }
            }}>Add key</Button>
            <Button type="button" size="sm" variant="ghost" className={rowButton} onClick={() => setAdding(false)}>Cancel</Button>
          </div>
        </div>
      ) : (
        <div className="border-t border-border px-4 py-2.5">
          <Button type="button" size="sm" variant="outline" className={rowButton} onClick={() => setAdding(true)}>+ Add a backup key</Button>
        </div>
      )}
      <ConfirmDialog open={!!removing} onOpenChange={(o) => { if (!o) setRemoving(null) }} destructive
        title={`Remove ${removing?.name ?? "this key"}?`}
        consequences={[
          { tone: "lost", text: "New backups are no longer encrypted to it." },
          { tone: "kept", text: "Backups already made stay readable with its private half." },
        ]}
        confirmLabel="Remove" onConfirm={async () => {
          if (!removing) return
          if (await perform(ctx.demo, () => removeRecipient(removing.id), "Key removed", "The key could not be removed")) reload?.()
        }} />
    </>
  )
}

function VaultList({ vault, ctx, reload }: { vault: VaultKeysResponse; ctx: SectionCtx; reload: () => void }) {
  const [confirm, setConfirm] = React.useState(false)
  const kit = vault.recovery_kit
  // The switch lives behind vault-keys and settings/recovery-kit; the
  // general settings resource does not carry it yet.
  const enabled = kit.enabled
  return (
    <>
      {vault.versions.map((v) => (
        <SettingsRow key={v.version} label={<span className="font-mono text-xs">{v.env} · {v.active ? "active" : "no longer mints"}</span>}
          description={!v.active && v.envelopes ? `still needed by ${v.envelopes} value${v.envelopes === 1 ? "" : "s"}` : undefined}>
          <StatusPill tone={v.active ? "success" : "muted"} label={v.version} />
        </SettingsRow>
      ))}
      <SettingsRow
        label={<span className="inline-flex items-center gap-1.5">Recovery kit · instance backups only
          <InfoTip label="About the recovery kit">Every vault key version rides inside instance backups, under AGE. Instance admins only. Workspace and crew backups never carry vault keys.</InfoTip></span>}
        description={<b className="font-semibold text-warn">Whoever holds a private backup key can read every secret.</b>}>
        {!kit.available && <StatusPill tone="warn" label="a key is missing on this server" />}
        <Button type="button" size="sm" variant="outline" className={rowButton}
          onClick={() => (enabled ? void perform(ctx.demo, () => setRecoveryKit(false), "Recovery kit off for new backups", "Could not change the recovery kit").then((o) => o && reload()) : setConfirm(true))}>
          {enabled ? "Turn off" : "Turn on…"}
        </Button>
      </SettingsRow>
      <ConfirmDialog open={confirm} onOpenChange={setConfirm} title="Put the vault keys in instance backups?"
        consequences={[
          { tone: "warn", text: "Whoever holds a private backup key can read every secret in those backups." },
          { tone: "kept", text: "Workspace and crew backups never carry vault keys." },
          { tone: "kept", text: "A restore on a new server unlocks credentials without re-entering them." },
        ]}
        confirmLabel="Turn on" onConfirm={async () => {
          if (await perform(ctx.demo, () => setRecoveryKit(true), "Recovery kit on for new instance backups", "Could not turn the recovery kit on")) reload()
        }} />
    </>
  )
}

const EVENTS: { key: keyof AlertEvents; label: string }[] = [
  { key: "failed", label: "a run fails" },
  { key: "incomplete", label: "contents are incomplete" },
  { key: "stale", label: "newest backup older than {h} h" },
  { key: "offsite", label: "off-site copy unreachable" },
  { key: "drill", label: "a drill fails or is overdue" },
]

const KIND_LABEL: Record<AlertChannel["kind"], string> = { chat: "chat", push: "push", incident: "incident", email: "email", webhook: "webhook" }

/** How the newest backup alert on a channel went, in words and a tone. */
export function deliveryLine(d: AlertDelivery | null | undefined): { text: string; tone: "ok" | "bad" | "muted" } {
  if (!d) return { text: "no alert sent yet", tone: "muted" }
  if (d.status === "sent") return { text: `last alert delivered ${formatWhen(d.at)}`, tone: "ok" }
  if (d.status === "failed") return { text: `last alert not delivered ${formatWhen(d.at)}: ${d.error || "no reason recorded"}`, tone: "bad" }
  return { text: "sending…", tone: "muted" }
}

type TestState = { busy: boolean; ok?: boolean; text?: string }

/**
 * Tell: where alerts go. The instance admins' inbox always; each notification
 * channel the server offers (workspace-wide, admitting System health, its
 * provider switched on) can be ticked to hear every alert too, and tested on
 * the spot. A chosen channel the server no longer offers stays listed so it
 * can be taken off, with why its alerts fail.
 */
export function AlertRoute({ settings, chosen, onChange, ctx }: {
  settings: BackupSettings
  chosen: string[]
  onChange: (next: string[]) => void
  ctx: SectionCtx
}) {
  const available = settings.available_channels ?? []
  const status = new Map((settings.channel_status ?? []).map((c) => [c.id, c]))
  const gone = chosen.filter((id) => !available.some((c) => c.id === id))
  const [tests, setTests] = React.useState<Record<string, TestState>>({})
  const toggle = (id: string, on: boolean) => onChange(on ? [...chosen, id] : chosen.filter((c) => c !== id))
  const test = async (c: AlertChannel) => {
    setTests((t) => ({ ...t, [c.id]: { busy: true } }))
    const res = await perform(ctx.demo, () => sendTestAlert(c.id), "", "The test alert could not be sent")
    if (!res) {
      setTests((t) => ({ ...t, [c.id]: { busy: false } }))
      return
    }
    if (res.ok) toast.success(`Test alert delivered to ${c.name}`)
    else toast.error(`Test alert to ${c.name} was not delivered: ${res.error ?? "unknown error"}`)
    setTests((t) => ({ ...t, [c.id]: { busy: false, ok: res.ok, text: res.ok ? "test delivered" : `test not delivered: ${res.error ?? "unknown error"}` } }))
  }
  return (
    <div className="flex flex-col gap-2 text-[13px]" data-slot="alert-route">
      <span className="flex items-center gap-2">
        <label className="flex items-center gap-2">
          <Checkbox checked disabled />
          Instance admins&apos; inbox
          <span className="text-muted-foreground"> · always</span>
        </label>
        <InfoTip label="About the inbox">From the inbox, each admin&apos;s own channels under System health carry it further.</InfoTip>
      </span>
      {available.map((c) => {
        const on = chosen.includes(c.id)
        const line = deliveryLine(status.get(c.id)?.last_delivery ?? c.last_delivery)
        const t = tests[c.id]
        return (
          <div key={c.id} className="flex flex-wrap items-center gap-x-2 gap-y-1" data-slot="alert-channel">
            <label className="flex items-center gap-2">
              <Checkbox checked={on} onCheckedChange={(v) => toggle(c.id, v === true)} />
              {c.name}
            </label>
            <StatusPill tone="muted" label={KIND_LABEL[c.kind] ?? c.kind} />
            <Button type="button" size="sm" variant="outline" className={rowButton} onClick={() => void test(c)} disabled={t?.busy}>{t?.busy ? "Sending…" : "Send test"}</Button>
            <span className={cn("text-[12px]", t?.text ? (t.ok ? "text-success" : "text-destructive") : line.tone === "bad" ? "text-destructive" : line.tone === "ok" ? "text-success" : "text-muted-foreground")}>
              {t?.text ?? line.text}
            </span>
          </div>
        )
      })}
      {gone.map((id) => {
        const line = deliveryLine(status.get(id)?.last_delivery)
        return (
          <div key={id} className="flex flex-wrap items-center gap-x-2 gap-y-1" data-slot="alert-channel-gone">
            <label className="flex items-center gap-2">
              <Checkbox checked onCheckedChange={() => toggle(id, false)} />
              <span className="font-mono text-[12px]">{id}</span>
            </label>
            <StatusPill tone="warn" label="no longer available" />
            <span className={cn("text-[12px]", line.tone === "bad" ? "text-destructive" : "text-muted-foreground")}>{line.text}</span>
          </div>
        )
      })}
      {available.length === 0 && gone.length === 0 && (
        <span className="text-xs text-muted-foreground" data-slot="alert-route-empty">
          No channel can carry backup alerts yet. Configure a provider in Admin › Notifications, then add a workspace-wide channel under Settings › Notifications.
        </span>
      )}
    </div>
  )
}

export function AlertsBody({ settings, incident, ctx, reload }: { settings: BackupSettings; incident: BackupIncident | null; ctx: SectionCtx; reload?: () => void }) {
  const [events, setEvents] = React.useState(settings.events)
  const [url, setUrl] = React.useState(settings.heartbeat_url ?? "")
  const [channels, setChannels] = React.useState(settings.channels ?? [])
  const offsiteKnown = settings.destinations.some((d) => d.kind !== "local" && d.available)
  const channelsDirty = JSON.stringify(channels) !== JSON.stringify(settings.channels ?? [])
  const changed = (Object.keys(events) as (keyof AlertEvents)[]).filter((k) => events[k] !== settings.events[k]).length
    + (url !== (settings.heartbeat_url ?? "") ? 1 : 0) + (channelsDirty ? 1 : 0)
  return (
    <>
      <SettingsCard icon={Bell} tint="var(--warn)" title="Alerts" description="Who hears when a backup goes wrong">
        <div className="flex flex-col gap-2 border-b border-border px-4 py-3">
          <span className="text-xs text-muted-foreground">What an instance admin sees in the inbox</span>
          <div className="flex flex-wrap items-start gap-2.5 rounded-lg border border-border bg-background px-3 py-2.5 text-[13px]" data-slot="inbox-preview">
            <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg bg-destructive/15 font-bold text-destructive" aria-hidden>!</span>
            <div className="min-w-0 flex-1">
              <div className="font-semibold">Backup needs attention</div>
              <div className="text-muted-foreground">{incident?.message ?? "Complete recovery backup failed. Last successful backup: 32 hours ago."}</div>
              <div className="mt-1.5 flex gap-1.5">
                <Button type="button" size="sm" variant="outline" className={rowButton} onClick={() => ctx.go("history", incident?.run_id ? { run: incident.run_id } : undefined)}>View failure</Button>
                <Button type="button" size="sm" className={rowButton} onClick={() => ctx.backUpNow()}>Retry</Button>
              </div>
            </div>
            <span className="shrink-0 text-xs text-muted-foreground-soft">
              {incident ? `${incident.count} failure${incident.count === 1 ? "" : "s"} · one incident · since ${formatWhen(incident.first_at)}` : "example"}
            </span>
          </div>
        </div>
        <SettingsRow label="Who" description="Not workspace owners, who may not administer the instance">
          <span className="text-xs">instance admins ({settings.instance_admins})</span>
        </SettingsRow>
        <div className="border-b border-border px-4 py-2.5 last:border-b-0">
          <div className="mb-2 text-[13px]">Tell <span className="text-[11px] text-muted-foreground-soft">· one message per incident change</span></div>
          <AlertRoute settings={settings} chosen={channels} onChange={setChannels} ctx={ctx} />
        </div>
      </SettingsCard>
      <SettingsCard icon={BellRing} tint="var(--destructive)" title="When to alert" description="Repeats join one incident per plan, resolved by the next good run">
        {EVENTS.map((e) => {
          const locked = e.key === "offsite" && !offsiteKnown
          return (
            <SettingsRow key={e.key} className={cn(locked && "opacity-60")}
              label={<label className="flex items-center gap-2">
                <Checkbox checked={events[e.key]} disabled={locked} onCheckedChange={(v) => setEvents({ ...events, [e.key]: v === true })} />
                {e.label.replace("{h}", String(settings.stale_alert_hours))}
              </label>}
              description={locked ? "Needs off-site storage first" : undefined}>
              {null}
            </SettingsRow>
          )
        })}
        <SettingsRow
          label={<span className="inline-flex items-center gap-1.5">Heartbeat URL
            <InfoTip label="About the heartbeat">An outside service you point here alerts when the pings stop — the case the inbox cannot report, because it is down too.</InfoTip></span>}
          description="Pinged after each successful run; not proof that every backup is healthy">
          <Input aria-label="Heartbeat URL" type="url" className={settingsControl} placeholder="https://hc.example.com/ping/…" value={url} onChange={(e) => setUrl(e.target.value)} />
        </SettingsRow>
      </SettingsCard>
      {/* The page's Save bar commits these; it says "Saved" and, on a failure, why not. */}
      <SettingsSaveBar label="Alerts" count={changed}
        onDiscard={() => { setEvents(settings.events); setUrl(settings.heartbeat_url ?? ""); setChannels(settings.channels ?? []) }}
        onSave={async () => {
          const patch = { events, heartbeat_url: url.trim() || null, ...(channelsDirty ? { channels } : {}) }
          if (await performSave(ctx.demo, () => saveBackupSettings(patch))) reload?.()
        }} />
    </>
  )
}
