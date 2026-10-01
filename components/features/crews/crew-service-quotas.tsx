"use client"

import { useEffect, useId, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

type Volume = { name: string; mount: string; quota_bytes?: number; generation?: number }
type Service = { name: string; image: string; ports?: string[]; env_refs?: string[]; volumes?: Volume[]; quota_enforced?: boolean }
const MIB = 1024 * 1024
// Quota volume contents cannot be captured yet, so the backup guard refuses
// rather than produce a bundle that silently omits service data.
const BACKUP_WARNING = "While a service keeps quota storage, backups of this crew and its workspace are refused until snapshot transport for quota volumes is available."

// Accept only the server's complete public representation. Private or unknown
// settings cannot be reconstructed by this quota editor.
function parseServices(raw: string | null | undefined): Service[] | null {
  if (!raw?.trim()) return []
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return null
    for (const service of value) {
      if (!service || typeof service !== "object" || typeof service.name !== "string" || typeof service.image !== "string") return null
      if (Object.keys(service).some(key => !["name", "image", "ports", "env_refs", "volumes", "quota_enforced"].includes(key))) return null
      if (service.quota_enforced !== undefined && typeof service.quota_enforced !== "boolean") return null
      if (service.volumes !== undefined && (!Array.isArray(service.volumes) || service.volumes.some((v: Volume) => !v || typeof v.name !== "string" || typeof v.mount !== "string" || Object.keys(v).some(key => !["name", "mount", "quota_bytes", "generation"].includes(key))))) return null
    }
    return value as Service[]
  } catch { return null }
}

export function CrewServiceQuotas({ servicesJSON, canManage, save }: {
  servicesJSON?: string | null
  canManage: boolean
  save: (body: Record<string, unknown>) => Promise<void>
}) {
  const prefix = useId()
  const original = parseServices(servicesJSON)
  const [draft, setDraft] = useState<Service[] | null>(() => original)
  const [acknowledged, setAcknowledged] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => { setDraft(parseServices(servicesJSON)); setAcknowledged(false); setError(null) }, [servicesJSON])

  const changed = JSON.stringify(draft) !== JSON.stringify(original)
  const usesQuotaStorage = draft?.some(service => service.quota_enforced && (service.volumes?.length ?? 0) > 0) ?? false
  const storageChanged = draft?.some((service, i) => {
    const before = original?.[i]
    return service.quota_enforced !== before?.quota_enforced || service.volumes?.some((volume, j) => (volume.generation ?? 1) !== (before?.volumes?.[j].generation ?? 1))
  }) ?? false

  function update(index: number, change: Partial<Service>) {
    setDraft(current => current?.map((service, i) => i === index ? { ...service, ...change } : service) ?? null)
    setError(null); setAcknowledged(false)
  }
  function updateVolume(index: number, volumeIndex: number, change: Partial<Volume>) {
    if (!draft) return
    update(index, { volumes: draft[index].volumes?.map((volume, i) => i === volumeIndex ? { ...volume, ...change } : volume) })
  }
  async function submit() {
    if (!canManage || !draft || pending || !changed) return
    for (const [i, service] of draft.entries()) {
      if (!service.quota_enforced) continue
      for (const [j, volume] of (service.volumes ?? []).entries()) {
        const bytes = volume.quota_bytes ?? 0
        const generation = volume.generation ?? 1
        if (!Number.isSafeInteger(bytes) || bytes < 32 * MIB || bytes > 65536 * MIB || bytes % MIB !== 0 || !Number.isSafeInteger(generation) || generation < 1) {
          setError("Use 32–65,536 MiB per volume and a positive whole-number generation."); return
        }
        const previous = original?.[i].volumes?.[j]
        if (original?.[i].quota_enforced && previous && (generation < (previous.generation ?? 1) || bytes !== previous.quota_bytes && generation === (previous.generation ?? 1))) {
          setError("Existing volumes keep their capacity. Increase the generation to create an empty volume with a different capacity."); return
        }
      }
    }
    if (storageChanged && !acknowledged) { setError("Acknowledge the storage change before saving."); return }
    setPending(true); setError(null)
    try {
      await save({ services_json: JSON.stringify(draft), expected_services_json: servicesJSON ?? "" })
      toast.success("Service disk policy saved")
    } catch {
      setError("Could not save the policy. Refresh the crew if its service configuration changed, then retry.")
    } finally { setPending(false) }
  }

  return <section className="space-y-3" aria-label="Service disk quotas">
    <h2 className="text-lg font-semibold">Service disk quotas</h2>
    <p className="text-sm text-muted-foreground">Enforced services have a read-only root and fixed disk capacity for each persistent volume. Starting them requires disk quota support on the server; unavailable support prevents startup. Capacity includes filesystem metadata, so usable data space is slightly lower.</p>
    {draft === null && <p role="status" className="text-sm text-muted-foreground">This service configuration contains private settings. Disk policy editing is unavailable here; existing settings are preserved.</p>}
    {draft?.length === 0 && <p className="text-sm text-muted-foreground">Declare services through a crew manifest or the CLI to configure their disk policy here.</p>}
    {draft?.map((service, i) => <div key={service.name} className="rounded-xl border p-4 space-y-3">
      <div><h3 className="font-medium">{service.name}</h3><p className="text-sm text-muted-foreground">{service.quota_enforced ? "Enforced disk quotas requested" : "Legacy trusted storage · root and persistent disks have no capacity limit"}</p></div>
      {canManage && <label className="flex items-center gap-2 text-sm coarse:min-h-12"><input type="checkbox" checked={!!service.quota_enforced} disabled={pending} aria-label={`${service.name}: Enforce disk quotas`} onChange={event => {
        const enabled = event.target.checked
        update(i, { quota_enforced: enabled, volumes: service.volumes?.map(volume => {
          if (enabled) return { ...volume, quota_bytes: volume.quota_bytes ?? 512 * MIB, generation: volume.generation ?? 1 }
          const { quota_bytes: _capacity, generation: _generation, ...legacy } = volume
          return legacy
        }) })
      }} />Enforce disk quotas</label>}
      {service.volumes?.map((volume, j) => <div key={volume.name} className="grid gap-3 sm:grid-cols-3 items-center">
        <div className="text-sm">{volume.name}<span className="block text-muted-foreground">{volume.mount}</span></div>
        {service.quota_enforced && <>
          <label htmlFor={`${prefix}-${i}-${j}-capacity`} className="text-sm">Capacity (MiB)
            <Input id={`${prefix}-${i}-${j}-capacity`} type="number" min={32} max={65536} step={1} value={(volume.quota_bytes ?? 0) / MIB} disabled={!canManage || pending} aria-label={`${service.name}/${volume.name}: Capacity in MiB`} onChange={event => updateVolume(i, j, { quota_bytes: Number(event.target.value) * MIB })} />
          </label>
          <label htmlFor={`${prefix}-${i}-${j}-generation`} className="text-sm">Volume generation
            <Input id={`${prefix}-${i}-${j}-generation`} type="number" min={1} step={1} value={volume.generation ?? 1} disabled={!canManage || pending} aria-label={`${service.name}/${volume.name}: Volume generation`} onChange={event => updateVolume(i, j, { generation: Number(event.target.value) })} />
          </label>
        </>}
      </div>)}
    </div>)}
    {usesQuotaStorage && !(canManage && changed && storageChanged) && <p role="note" className="text-sm text-muted-foreground">{BACKUP_WARNING}</p>}
    {canManage && changed && storageChanged && <label className="flex items-start gap-2 text-sm"><input type="checkbox" className="mt-1" checked={acknowledged} disabled={pending} onChange={event => setAcknowledged(event.target.checked)} /><span>I understand that changing storage policy or generation recreates the service and switches volumes. New quota generations start empty; existing data is not copied automatically. {usesQuotaStorage && BACKUP_WARNING}</span></label>}
    {error && <p role="alert" className="text-sm">{error}</p>}
    {canManage && draft && draft.length > 0 && <Button className="coarse:h-12" disabled={!changed || pending} onClick={() => void submit()}>{pending ? "Saving…" : "Save disk policy"}</Button>}
  </section>
}
