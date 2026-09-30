"use client"

import * as React from "react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { SettingsCard } from "@/components/features/settings/shared"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Chip, Eyebrow, FieldRow, Gate, InlineInput, ItemRow, SmallButton } from "./backups-kit"
import {
  formatWhen, shortKey, type AlertChannel, type AlertDelivery, type AlertEvents, type BackupIncident, type BackupRecipient, type BackupSettings, type VaultKeysResponse,
} from "./backups-model"
import {
  RECOVERY_SHEET_HREF, addRecipient, removeRecipient, saveBackupSettings, sendTestAlert, setRecoveryKit,
  useBackupSettings, useIncidents, useRecipients, useVaultKeys,
} from "./use-backup-settings"
import { perform } from "./use-backups-data"
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
  return (
    <>
      <SettingsCard title="Backup keys (AGE)" description="public keys; the private halves stay off this server"
        actions={<SmallButton asChild><a href={RECOVERY_SHEET_HREF} download="crewship-recovery-sheet.md">Recovery sheet</a></SmallButton>}>
        <Gate resource={recipients} what="Backup keys" skeleton="h-[96px]">
          {(list) => <RecipientsList list={list} ctx={ctx} reload={recipients.reload} />}
        </Gate>
      </SettingsCard>
      <SettingsCard title="Vault keys" description="open credentials, webhook secrets and integration settings in restored data">
        <Gate resource={vault} what="Vault keys" skeleton="h-[96px]">
          {(v) => <VaultList vault={v} ctx={ctx} reload={() => vault.reload()} />}
        </Gate>
      </SettingsCard>
      <SettingsCard title="Alerts">
        <Gate resource={settings} what="Alert settings" skeleton="h-[200px]">
          {(s) => <AlertsBody settings={s} incident={incidents.data?.find((i) => i.state === "open") ?? null} ctx={ctx} reload={settings.reload} />}
        </Gate>
      </SettingsCard>
    </>
  )
}

export function RecipientsList({ list, ctx, reload }: { list: BackupRecipient[]; ctx: SectionCtx; reload?: () => void }) {
  const [removing, setRemoving] = React.useState<BackupRecipient | null>(null)
  const [adding, setAdding] = React.useState(false)
  const [form, setForm] = React.useState({ name: "", public_key: "", holder: "" })
  const valid = form.name.trim() && /^age1[0-9a-z]{20,}$/.test(form.public_key.trim())
  return (
    <>
      {list.length === 0 && <p className="px-4 py-3 text-[12.5px] text-warn">No backup key yet. Every backup is encrypted, so add one before the first run.</p>}
      {list.map((r) => (
        <ItemRow key={r.id} title={`${r.name} · ${shortKey(r.public_key)}`}
          detail={[r.holder, r.used_by?.length ? `used by ${r.used_by.join(", ")}` : ""].filter(Boolean).join(" · ")}
          action={<SmallButton onClick={() => setRemoving(r)} disabled={list.length === 1 || !!r.used_by?.length}
            title={list.length === 1 ? "The last key cannot go: every backup is encrypted" : r.used_by?.length ? `Used by ${r.used_by.join(", ")}: change the plan first` : undefined}>Remove</SmallButton>} />
      ))}
      {adding ? (
        <div className="flex flex-col gap-2 border-t border-border px-4 py-3 text-[13px]">
          <input aria-label="Key name" placeholder="Name, e.g. ops-2027" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })}
            className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 coarse:h-[2.75rem]" />
          <input aria-label="Public key" placeholder="age1…" value={form.public_key} onChange={(e) => setForm({ ...form, public_key: e.target.value })} spellCheck={false}
            className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 font-mono text-[12px] coarse:h-[2.75rem]" />
          <input aria-label="Who holds the private key" placeholder="Who holds the private key" value={form.holder} onChange={(e) => setForm({ ...form, holder: e.target.value })}
            className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 coarse:h-[2.75rem]" />
          <div className="flex gap-1.5">
            <SmallButton primary disabled={!valid} onClick={async () => {
              const out = await perform(ctx.demo, () => addRecipient({ name: form.name.trim(), public_key: form.public_key.trim(), holder: form.holder.trim() }), "Key added · new backups are encrypted to it", "The key could not be added")
              if (out) { setAdding(false); setForm({ name: "", public_key: "", holder: "" }); reload?.() }
            }}>Add key</SmallButton>
            <SmallButton onClick={() => setAdding(false)}>Cancel</SmallButton>
          </div>
        </div>
      ) : (
        <div className="border-t border-border px-4 py-2.5"><SmallButton onClick={() => setAdding(true)}>+ Add a backup key</SmallButton></div>
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
        <ItemRow key={v.version} lead={<Chip tone={v.active ? "ok" : "muted"}>{v.version}</Chip>}
          title={`${v.env} · ${v.active ? "active" : "no longer mints"}`}
          detail={!v.active && v.envelopes ? `still needed by ${v.envelopes} value${v.envelopes === 1 ? "" : "s"}` : undefined} />
      ))}
      <ItemRow title="Recovery kit · instance backups only"
        detail={<>every key version rides inside, under AGE. Instance admins only. <b className="font-semibold text-warn">Whoever holds a private backup key can read every secret.</b></>}
        action={
          <>
            {!kit.available && <Chip tone="warn">a key is missing on this server</Chip>}
            <SmallButton onClick={() => (enabled ? void perform(ctx.demo, () => setRecoveryKit(false), "Recovery kit off for new backups", "Could not change the recovery kit").then((o) => o && reload()) : setConfirm(true))}>
              {enabled ? "Turn off" : "Turn on…"}
            </SmallButton>
          </>
        } />
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
    <div className="flex flex-col gap-1.5" data-slot="alert-route">
      <label className="flex items-center gap-1.5">
        <input type="checkbox" checked disabled />
        Instance admins&apos; inbox
        <span className="text-muted-foreground"> · always, and from there each admin&apos;s own channels under System health</span>
      </label>
      {available.map((c) => {
        const on = chosen.includes(c.id)
        const line = deliveryLine(status.get(c.id)?.last_delivery ?? c.last_delivery)
        const t = tests[c.id]
        return (
          <div key={c.id} className="flex flex-wrap items-center gap-x-2 gap-y-1" data-slot="alert-channel">
            <label className="flex items-center gap-1.5">
              <input type="checkbox" checked={on} onChange={(e) => toggle(c.id, e.target.checked)} />
              {c.name}
            </label>
            <Chip tone="muted">{KIND_LABEL[c.kind] ?? c.kind}</Chip>
            <SmallButton onClick={() => void test(c)} disabled={t?.busy}>{t?.busy ? "Sending…" : "Send test"}</SmallButton>
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
            <label className="flex items-center gap-1.5">
              <input type="checkbox" checked onChange={() => toggle(id, false)} />
              <span className="font-mono text-[12px]">{id}</span>
            </label>
            <Chip tone="warn">no longer available</Chip>
            <span className={cn("text-[12px]", line.tone === "bad" ? "text-destructive" : "text-muted-foreground")}>{line.text}</span>
          </div>
        )
      })}
      {available.length === 0 && gone.length === 0 && (
        <span className="text-muted-foreground" data-slot="alert-route-empty">
          No notification channel can carry backup alerts yet. Configure a provider in Admin › Notifications, then add a workspace-wide channel under Settings › Notifications.
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
  const dirty = JSON.stringify(events) !== JSON.stringify(settings.events) || url !== (settings.heartbeat_url ?? "") || channelsDirty
  return (
    <>
      <div className="flex flex-col gap-2 px-3.5 py-3">
        <Eyebrow>What an instance admin sees in the inbox</Eyebrow>
        <div className="flex flex-wrap items-start gap-2.5 rounded-[10px] border border-border bg-background px-3 py-2.5 text-[13px]" data-slot="inbox-preview">
          <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg bg-destructive/15 font-bold text-destructive" aria-hidden>!</span>
          <div className="min-w-0 flex-1">
            <div className="font-semibold">Backup needs attention</div>
            <div className="text-muted-foreground">{incident?.message ?? "Complete recovery backup failed. Last successful backup: 32 hours ago."}</div>
            <div className="mt-1.5 flex gap-1.5">
              <SmallButton onClick={() => ctx.go("history", incident?.run_id ? { run: incident.run_id } : undefined)}>View failure</SmallButton>
              <SmallButton primary onClick={() => ctx.backUpNow()}>Retry</SmallButton>
            </div>
          </div>
          <span className="shrink-0 text-[12px] text-muted-foreground-soft">
            {incident ? `${incident.count} failure${incident.count === 1 ? "" : "s"} · one incident · since ${formatWhen(incident.first_at)}` : "example"}
          </span>
        </div>
      </div>
      <FieldRow label="Who">instance admins ({settings.instance_admins}) · <span className="text-muted-foreground">not workspace owners, who may not administer the instance</span></FieldRow>
      <FieldRow label="Tell" hint="one message per incident change">
        <AlertRoute settings={settings} chosen={channels} onChange={setChannels} ctx={ctx} />
      </FieldRow>
      <FieldRow label="When">
        <span className="flex flex-wrap gap-x-3.5 gap-y-1.5">
          {EVENTS.map((e) => {
            const locked = e.key === "offsite" && !offsiteKnown
            return (
              <label key={e.key} className={cn("flex items-center gap-1.5", locked && "opacity-60")}>
                <input type="checkbox" checked={events[e.key]} disabled={locked} onChange={(ev) => setEvents({ ...events, [e.key]: ev.target.checked })} />
                {e.label.replace("{h}", String(settings.stale_alert_hours))}
              </label>
            )
          })}
        </span>
      </FieldRow>
      <FieldRow label="Repeats">one incident per plan, updated on each failure, resolved by the next good run</FieldRow>
      <FieldRow label="Server down" hint="the inbox is down too">
        ping <InlineInput aria-label="Heartbeat URL" type="url" className="w-full max-w-[15rem]" placeholder="https://hc.example.com/ping/…" value={url} onChange={(e) => setUrl(e.target.value)} />
        after every good run; the outside service alerts when pings stop
      </FieldRow>
      {dirty && (
        <div className="flex items-center gap-2 border-t border-border px-4 py-2.5">
          <SmallButton primary onClick={async () => {
            const patch = { events, heartbeat_url: url.trim() || null, ...(channelsDirty ? { channels } : {}) }
            if (await perform(ctx.demo, () => saveBackupSettings(patch), "Alerts saved", "The alerts could not be saved")) reload?.()
          }}>Save alerts</SmallButton>
          <SmallButton onClick={() => { setEvents(settings.events); setUrl(settings.heartbeat_url ?? ""); setChannels(settings.channels ?? []) }}>Discard</SmallButton>
        </div>
      )}
    </>
  )
}
