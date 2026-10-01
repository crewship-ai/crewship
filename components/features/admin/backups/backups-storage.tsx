"use client"

import * as React from "react"
import { toast } from "sonner"

import { SettingsCard } from "@/components/features/settings/shared"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Chip, FieldRow, Gate, InlineInput, ItemRow, LocalOnlyBar, SmallButton, Unavailable } from "./backups-kit"
import { formatSize, formatWhen, verifiedByText, type BackupSettings, type NewOffsiteDestination, type OffsiteDestination, type SpaceInfo } from "./backups-model"
import { addDestination, removeDestination, saveBackupSettings, testDestination, useBackupSettings, useDestinations } from "./use-backup-settings"
import { useBackupsOverview } from "./use-backups-overview"
import { perform } from "./use-backups-data"
import type { SectionCtx } from "./backups-console"

/**
 * Backups › Storage (instance setting): where copies are kept — this server
 * and the S3-compatible stores added here — how much room a run and a
 * restore need, and how hard a backup may push the server.
 */
export function BackupsStorage({ ctx }: { ctx: SectionCtx }) {
  const settings = useBackupSettings()
  const destinations = useDestinations()
  const overview = useBackupsOverview("instance", ctx.selected, ctx.workspaces)
  const reload = () => { settings.reload(); destinations.reload() }
  return (
    <>
      {settings.status === "ready" && settings.data ? (
        <StorageBody settings={settings.data} destinations={destinations.data ?? []} destinationsReady={destinations.status === "ready"}
          space={overview.data?.space ?? null} ctx={ctx} reload={reload} />
      ) : (
        <>
          <LocalOnlyBar />
          <Gate resource={settings} what="Storage settings">{() => null}</Gate>
          {overview.data && <RoomCard space={overview.data.space} />}
        </>
      )}
    </>
  )
}

export function StorageBody({ settings, destinations = [], destinationsReady = true, space, ctx, reload }: {
  settings: BackupSettings
  destinations?: OffsiteDestination[]
  destinationsReady?: boolean
  space: SpaceInfo | null
  ctx: SectionCtx
  reload?: () => void
}) {
  const [limits, setLimits] = React.useState(settings.limits)
  const [adding, setAdding] = React.useState(false)
  const [removing, setRemoving] = React.useState<OffsiteDestination | null>(null)
  const dirty = JSON.stringify(limits) !== JSON.stringify(settings.limits)
  const local = settings.destinations.find((d) => d.kind === "local")
  const verifiedOffsite = settings.destinations.some((d) => d.kind !== "local" && d.verified)
  const setLimit = (k: keyof BackupSettings["limits"]) => (e: React.ChangeEvent<HTMLInputElement>) => setLimits((l) => ({ ...l, [k]: Math.max(0, Number(e.target.value) || 0) }))

  return (
    <>
      {!verifiedOffsite && <LocalOnlyBar />}
      <section aria-label="Destinations" className="overflow-hidden rounded-card border border-border bg-card">
        <ItemRow lead={<Chip tone="ok">local</Chip>}
          title={`This server · ${local?.path ?? settings.local_path ?? "~/.crewship/backups"}`}
          detail={`${formatSize(local?.used_bytes ?? space?.backups_bytes ?? null)} · staging for every upload`} />
        {destinations.map((d) => (
          <ItemRow key={d.id} lead={<Chip tone={d.copies > 0 ? "ok" : "muted"}>S3</Chip>}
            title={<>{d.name} · <span className="font-mono text-[12px]">{d.bucket}{d.prefix ? `/${d.prefix}` : ""}</span></>}
            detail={destinationDetail(d)}
            action={
              <>
                <SmallButton onClick={() => void perform(ctx.demo, () => testDestination(d.id), "", "The connection could not be tested").then((r) => {
                  if (!r) return
                  if (r.ok) toast.success(`${d.name}: connection ok`)
                  else toast.error(`${d.name}: ${r.error ?? "the connection test failed"}`)
                  reload?.()
                })}>Test</SmallButton>
                <SmallButton onClick={() => setRemoving(d)} disabled={d.used_by.length > 0}
                  title={d.used_by.length ? `Used by ${d.used_by.join(", ")}: change the plan first` : undefined}>Remove</SmallButton>
              </>
            } />
        ))}
        {adding ? (
          <AddDestinationForm ctx={ctx} onDone={(ok) => { setAdding(false); if (ok) reload?.() }} />
        ) : (
          <ItemRow lead={<Chip tone="muted">S3</Chip>} title="+ Add S3-compatible storage"
            detail="R2, B2, Wasabi, MinIO, AWS · counted as a copy only after the upload is checked"
            action={<SmallButton onClick={() => setAdding(true)} disabled={!destinationsReady}>Add…</SmallButton>} />
        )}
        <ItemRow className="opacity-60" lead={<Chip tone="muted">Drive</Chip>} title="Google Drive (after S3)" detail="same transfer layer" />
      </section>

      {space ? <RoomCard space={space} /> : <SettingsCard title="Room"><Unavailable what="disk figures" className="m-3" /></SettingsCard>}

      <SettingsCard title="Limits" description="a backup never crowds out the work it protects">
        <FieldRow label="At once"><InlineInput aria-label="Backup runs at once" type="number" min={1} value={limits.concurrency} onChange={setLimit("concurrency")} />backup run</FieldRow>
        <FieldRow label="CPU"><InlineInput aria-label="CPU cores" type="number" min={1} value={limits.cpu_cores} onChange={setLimit("cpu_cores")} />cores for packing and compression</FieldRow>
        <FieldRow label="Disk"><InlineInput aria-label="Disk MB/s" type="number" min={0} value={limits.disk_mbps} onChange={setLimit("disk_mbps")} />MB/s <span className="text-muted-foreground">· 0 is no limit</span></FieldRow>
        <FieldRow label="Upload"><InlineInput aria-label="Upload MB/s" type="number" min={0} value={limits.upload_mbps} onChange={setLimit("upload_mbps")} />MB/s <span className="text-muted-foreground">· 0 is no limit</span></FieldRow>
        <FieldRow label="Staging">encrypted before it touches disk; wiped after the run and on the next start after a crash</FieldRow>
        {dirty && (
          <div className="flex items-center gap-2 border-t border-border px-4 py-2.5">
            <SmallButton primary onClick={async () => { if (await perform(ctx.demo, () => saveBackupSettings({ limits }), "Limits saved", "The limits could not be saved")) reload?.() }}>Save limits</SmallButton>
            <SmallButton onClick={() => setLimits(settings.limits)}>Discard</SmallButton>
          </div>
        )}
      </SettingsCard>

      <ConfirmDialog open={!!removing} onOpenChange={(o) => { if (!o) setRemoving(null) }} destructive
        title={`Remove ${removing?.name ?? "this destination"}?`}
        consequences={[
          { tone: "lost", text: "New backups are no longer copied there, and it no longer counts as an off-site copy." },
          { tone: "kept", text: "The copies already uploaded stay in the bucket." },
        ]}
        confirmLabel="Remove" onConfirm={async () => {
          if (!removing) return
          if (await perform(ctx.demo, () => removeDestination(removing.id), "Destination removed", "The destination could not be removed")) reload?.()
        }} />
    </>
  )
}

function destinationDetail(d: OffsiteDestination): string {
  const parts: string[] = []
  parts.push(d.copies > 0 ? `${d.copies} checked cop${d.copies === 1 ? "y" : "ies"} · ${formatSize(d.copy_bytes)}` : "not yet counted: no checked upload")
  if (d.last_test_error) parts.push(`last test failed: ${d.last_test_error}`)
  else if (d.last_verified_at) parts.push(`last checked ${formatWhen(d.last_verified_at)} · ${verifiedByText(d.last_verified_by)}`)
  parts.push(d.used_by.length ? `used by ${d.used_by.join(", ")}` : "no plan copies here yet · choose it under Schedules › Where")
  return parts.join(" · ")
}

const EMPTY_DEST: NewOffsiteDestination = {
  name: "", endpoint: "", region: "", bucket: "", prefix: "", access_key_id: "", secret_access_key: "", path_style: false, allow_private_network: false,
}

/** The S3 form. The server tests the connection before it stores anything. */
export function AddDestinationForm({ ctx, onDone }: { ctx: SectionCtx; onDone: (ok: boolean) => void }) {
  const [form, setForm] = React.useState<NewOffsiteDestination>(EMPTY_DEST)
  const [busy, setBusy] = React.useState(false)
  const set = <K extends keyof NewOffsiteDestination>(k: K, v: NewOffsiteDestination[K]) => setForm((f) => ({ ...f, [k]: v }))
  const valid = /^https?:\/\/\S+$/.test(form.endpoint.trim()) && form.bucket.trim() && form.access_key_id.trim() && form.secret_access_key.trim()
  const field = (k: "name" | "endpoint" | "region" | "bucket" | "prefix" | "access_key_id", label: string, placeholder: string, mono = false) => (
    <input aria-label={label} placeholder={placeholder} value={form[k]} spellCheck={false} onChange={(e) => set(k, e.target.value)}
      className={`h-8 rounded-md border border-control-border bg-surface-subtle px-2 coarse:h-[2.75rem] ${mono ? "font-mono text-[12px]" : ""}`} />
  )
  return (
    <div data-slot="add-destination" className="flex flex-col gap-2 border-b border-border px-4 py-3 text-[13px]">
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {field("endpoint", "Endpoint", "https://<account>.r2.cloudflarestorage.com", true)}
        {field("region", "Region", "Region (empty: us-east-1, R2: auto)")}
        {field("bucket", "Bucket", "Bucket", true)}
        {field("prefix", "Prefix", "Prefix, e.g. crewship/prod", true)}
        {field("access_key_id", "Access key ID", "Access key ID", true)}
        <input aria-label="Secret access key" placeholder="Secret access key" type="password" autoComplete="off" value={form.secret_access_key}
          onChange={(e) => set("secret_access_key", e.target.value)}
          className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 font-mono text-[12px] coarse:h-[2.75rem]" />
        {field("name", "Name", "Name (default: bucket/prefix)")}
      </div>
      <label className="flex items-center gap-1.5"><input type="checkbox" checked={form.path_style} onChange={(e) => set("path_style", e.target.checked)} />
        Path-style addressing <span className="text-muted-foreground">· MinIO and most self-hosted stores</span></label>
      <label className="flex items-center gap-1.5"><input type="checkbox" checked={form.allow_private_network} onChange={(e) => set("allow_private_network", e.target.checked)} />
        Allow a private network address</label>
      {form.allow_private_network && (
        <p className="text-[12.5px] text-warn">The endpoint may then be a loopback or LAN address, over plain http. Cloud metadata and link-local addresses stay blocked.</p>
      )}
      <p className="text-[12px] text-muted-foreground">The secret is sealed with this server&apos;s vault key and never shown again. Saving first writes, checks and deletes one small object.</p>
      <div className="flex gap-1.5">
        <SmallButton primary disabled={!valid || busy} onClick={async () => {
          setBusy(true)
          const out = await perform(ctx.demo, () => addDestination({ ...form, name: form.name.trim(), endpoint: form.endpoint.trim(), bucket: form.bucket.trim() }),
            "Storage added · connection checked", "The storage could not be added")
          setBusy(false)
          if (out) onDone(true)
        }}>{busy ? "Testing…" : "Test and add"}</SmallButton>
        <SmallButton onClick={() => onDone(false)}>Cancel</SmallButton>
      </div>
    </div>
  )
}

function RoomCard({ space }: { space: SpaceInfo }) {
  return (
    <SettingsCard title="Room">
      <FieldRow label="Free now">{formatSize(space.free_bytes)} of {formatSize(space.total_bytes)}</FieldRow>
      <FieldRow label="A run needs" detail="a run that would leave less than 10 % free does not start, and says why">{formatSize(space.staging_need_bytes)} for staging beside the finished backup</FieldRow>
      <FieldRow label="A restore needs" detail="checked before a restore begins">{formatSize(space.restore_need_bytes)} for the largest backup</FieldRow>
    </SettingsCard>
  )
}
