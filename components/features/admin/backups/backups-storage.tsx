"use client"

import * as React from "react"
import { toast } from "sonner"
import { Gauge, HardDrive, Info, Server } from "lucide-react"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsRow, SettingsSaveBar, SettingsSummary, SummaryItem, settingsControl, controlHeight } from "@/components/features/settings/shared"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Input } from "@/components/ui/input"
import { StatusPill } from "@/components/ui/status-pill"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { Gate, Unavailable } from "./backups-kit"
import { formatSize, formatWhen, verifiedByText, type BackupSettings, type NewOffsiteDestination, type OffsiteDestination, type SpaceInfo } from "./backups-model"
import { addDestination, removeDestination, saveBackupSettings, testDestination, useBackupSettings, useDestinations } from "./use-backup-settings"
import { useBackupsOverview } from "./use-backups-overview"
import { perform, performSave } from "./use-backups-data"
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
          <Gate resource={settings} what="Storage settings">{() => null}</Gate>
          {overview.data && <RoomCard space={overview.data.space} />}
        </>
      )}
    </>
  )
}

/** The "Instance" badge the instance-only pages carry (as Security does). */
export function InstanceBadge() {
  return (
    <span className="rounded-full bg-primary/10 px-2 font-mono text-micro text-primary-hover" title="Instance setting · applies to every backup plan">
      Instance
    </span>
  )
}

/** A small (i) whose tooltip carries the explanation that does not fit a row. */
export function InfoTip({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button type="button" aria-label={label} className="inline-flex shrink-0 cursor-help align-middle text-muted-foreground hover:text-foreground">
            <Info className="h-3.5 w-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-xs text-label">{children}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

const rowButton = "h-7 px-2.5 text-xs coarse:h-[2.75rem]"

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
  const changed = (Object.keys(limits) as (keyof BackupSettings["limits"])[]).filter((k) => limits[k] !== settings.limits[k]).length
  const local = settings.destinations.find((d) => d.kind === "local")
  const verifiedOffsite = settings.destinations.some((d) => d.kind !== "local" && d.verified)
  const setLimit = (k: keyof BackupSettings["limits"]) => (e: React.ChangeEvent<HTMLInputElement>) => setLimits((l) => ({ ...l, [k]: Math.max(0, Number(e.target.value) || 0) }))
  const count = 1 + destinations.length

  const limitRow = (k: keyof BackupSettings["limits"], label: string, unit: string, description?: string, min = 0) => (
    <SettingsRow label={label} description={description}>
      <Input aria-label={label} type="number" min={min} value={limits[k]} onChange={setLimit(k)} className={cn(settingsControl, "sm:w-24")} />
      <span className="w-24 text-xs text-muted-foreground">{unit}</span>
    </SettingsRow>
  )

  return (
    <>
      <SettingsSummary>
        <InstanceBadge />
        <SummaryItem n={count}>destination{count === 1 ? "" : "s"}</SummaryItem>
        {verifiedOffsite
          ? <SummaryItem tone="success">off-site copy verified</SummaryItem>
          : <SummaryItem tone="danger">local copy only</SummaryItem>}
        {space && <SummaryItem n={formatSize(space.free_bytes)}>free</SummaryItem>}
      </SettingsSummary>

      <SettingsCard icon={Server} tint="var(--info)" title="Destinations" description="Where copies are kept"
        actions={!adding && <Button type="button" size="sm" variant="outline" className={rowButton} onClick={() => setAdding(true)} disabled={!destinationsReady}>Add…</Button>}>
        {!verifiedOffsite && (
          <SettingsRow label={<span className="text-warn">Local copy only. Losing this server is not covered.</span>}
            description="Every backup sits on the same disk as the data it protects">
            <StatusPill tone="warn" label="no off-site copy" />
          </SettingsRow>
        )}
        <SettingsRow label={`This server · ${local?.path ?? settings.local_path ?? "~/.crewship/backups"}`}
          description={`${formatSize(local?.used_bytes ?? space?.backups_bytes ?? null)} · staging for every upload`}>
          <StatusPill tone="success" label="local" />
        </SettingsRow>
        {destinations.map((d) => (
          <SettingsRow key={d.id}
            label={<>{d.name} · <span className="font-mono text-label">{d.bucket}{d.prefix ? `/${d.prefix}` : ""}</span></>}
            description={destinationDetail(d)}>
            <StatusPill tone={d.last_test_error ? "danger" : d.copies > 0 ? "success" : "muted"} label="S3" />
            <Button type="button" size="sm" variant="outline" className={rowButton}
              onClick={() => void perform(ctx.demo, () => testDestination(d.id), "", "The connection could not be tested").then((r) => {
                if (!r) return
                if (r.ok) toast.success(`${d.name}: connection ok`)
                else toast.error(`${d.name}: ${r.error ?? "the connection test failed"}`)
                reload?.()
              })}>Test</Button>
            <Button type="button" size="sm" variant="outline" className={rowButton} onClick={() => setRemoving(d)} disabled={d.used_by.length > 0}
              title={d.used_by.length ? `Used by ${d.used_by.join(", ")}: change the plan first` : undefined}>Remove</Button>
          </SettingsRow>
        ))}
        {adding ? (
          <AddDestinationForm ctx={ctx} onDone={(ok) => { setAdding(false); if (ok) reload?.() }} />
        ) : (
          <SettingsRow label="+ Add S3-compatible storage" description="R2, B2, Wasabi, MinIO, AWS · counted once an upload is checked">
            <StatusPill tone="muted" label="S3" />
          </SettingsRow>
        )}
        <SettingsRow className="opacity-60" label="Google Drive (after S3)" description="Same transfer layer">
          <StatusPill tone="muted" label="later" />
        </SettingsRow>
      </SettingsCard>

      {space ? <RoomCard space={space} /> : (
        <SettingsCard icon={HardDrive} tint="var(--success)" title="Room"><Unavailable what="disk figures" className="m-3" /></SettingsCard>
      )}

      <SettingsCard icon={Gauge} tint="var(--purple)" title="Limits" description="A backup never crowds out the work it protects">
        {limitRow("concurrency", "Backup runs at once", "at once", undefined, 1)}
        {limitRow("cpu_cores", "CPU cores", "cores", "For packing and compression", 1)}
        {limitRow("disk_mbps", "Disk MB/s", "MB/s", "0 is no limit")}
        {limitRow("upload_mbps", "Upload MB/s", "MB/s", "0 is no limit")}
        <SettingsRow label={<span className="inline-flex items-center gap-1.5">Staging <InfoTip label="About staging">Encrypted before it touches disk; wiped after the run and on the next start after a crash.</InfoTip></span>}
          description="Encrypted before it touches disk">
          <StatusPill tone="muted" label="automatic" />
        </SettingsRow>
      </SettingsCard>
      {/* The page's Save bar commits these; it says "Saved" and, on a failure, why not. */}
      <SettingsSaveBar label="Backup limits" count={changed} onDiscard={() => setLimits(settings.limits)}
        onSave={async () => { if (await performSave(ctx.demo, () => saveBackupSettings({ limits }))) reload?.() }} />

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
    <Input aria-label={label} placeholder={placeholder} value={form[k]} spellCheck={false} onChange={(e) => set(k, e.target.value)}
      className={cn(controlHeight, mono && "font-mono")} />
  )
  return (
    <div data-slot="add-destination" className="flex flex-col gap-2.5 border-b border-border px-4 py-3 text-control last:border-b-0">
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {field("endpoint", "Endpoint", "https://<account>.r2.cloudflarestorage.com", true)}
        {field("region", "Region", "Region (empty: us-east-1, R2: auto)")}
        {field("bucket", "Bucket", "Bucket", true)}
        {field("prefix", "Prefix", "Prefix, e.g. crewship/prod", true)}
        {field("access_key_id", "Access key ID", "Access key ID", true)}
        <Input aria-label="Secret access key" placeholder="Secret access key" type="password" autoComplete="off" value={form.secret_access_key}
          onChange={(e) => set("secret_access_key", e.target.value)} className={cn(controlHeight, "font-mono")} />
        {field("name", "Name", "Name (default: bucket/prefix)")}
      </div>
      <label className="flex items-center gap-2">
        <Checkbox checked={form.path_style} onCheckedChange={(v) => set("path_style", v === true)} />
        Path-style addressing <span className="text-muted-foreground">· MinIO and most self-hosted stores</span>
      </label>
      <label className="flex items-center gap-2">
        <Checkbox checked={form.allow_private_network} onCheckedChange={(v) => set("allow_private_network", v === true)} />
        Allow a private network address
      </label>
      {form.allow_private_network && (
        <p className="text-xs text-warn">The endpoint may then be a loopback or LAN address, over plain http. Cloud metadata and link-local addresses stay blocked.</p>
      )}
      <p className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        The secret is sealed and never shown again.
        <InfoTip label="About the secret">It is sealed with this server&apos;s vault key. Saving first writes, checks and deletes one small object in the bucket.</InfoTip>
      </p>
      <div className="flex gap-1.5">
        <Button type="button" size="sm" className={rowButton} disabled={!valid || busy} onClick={async () => {
          setBusy(true)
          const out = await perform(ctx.demo, () => addDestination({ ...form, name: form.name.trim(), endpoint: form.endpoint.trim(), bucket: form.bucket.trim() }),
            "Storage added · connection checked", "The storage could not be added")
          setBusy(false)
          if (out) onDone(true)
        }}>{busy ? "Testing…" : "Test and add"}</Button>
        <Button type="button" size="sm" variant="ghost" className={rowButton} onClick={() => onDone(false)}>Cancel</Button>
      </div>
    </div>
  )
}

function RoomCard({ space }: { space: SpaceInfo }) {
  return (
    <SettingsCard icon={HardDrive} tint="var(--success)" title="Room" description="A run that would leave under 10 % free does not start">
      <SettingsRow label="Free now"><span className="font-mono text-xs">{formatSize(space.free_bytes)} of {formatSize(space.total_bytes)}</span></SettingsRow>
      <SettingsRow label="A run needs" description="Staging beside the finished backup"><span className="font-mono text-xs">{formatSize(space.staging_need_bytes)}</span></SettingsRow>
      <SettingsRow label="A restore needs" description="Checked before a restore begins"><span className="font-mono text-xs">{formatSize(space.restore_need_bytes)}</span></SettingsRow>
    </SettingsCard>
  )
}
