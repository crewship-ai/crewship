"use client"

import { useEffect, useState } from "react"
import { HardDrive, Cpu, Clock } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"

interface CrewContainerConfigProps {
  memoryMb: number
  cpus: number
  ttlHours: number | null
  canEdit: boolean
  onSave: (config: { container_memory_mb: number; container_cpus: number; container_ttl_hours: number | null }) => Promise<void>
}

export function CrewContainerConfig({ memoryMb, cpus, ttlHours, canEdit, onSave }: CrewContainerConfigProps) {
  const [mem, setMem] = useState(memoryMb)
  const [cpu, setCpu] = useState(cpus)
  const [ttl, setTtl] = useState<string>(ttlHours?.toString() ?? "")
  const [saving, setSaving] = useState(false)

  useEffect(() => { setMem(memoryMb) }, [memoryMb])
  useEffect(() => { setCpu(cpus) }, [cpus])
  useEffect(() => { setTtl(ttlHours?.toString() ?? "") }, [ttlHours])

  const hasChanges = mem !== memoryMb || cpu !== cpus || (ttl === "" ? ttlHours !== null : parseInt(ttl) !== ttlHours)

  async function handleSave() {
    setSaving(true)
    try {
      if (!Number.isInteger(mem) || mem < 256 || mem > 32768) {
        toast.error("Memory must be between 256 MB and 32768 MB")
        return
      }
      if (!Number.isFinite(cpu) || cpu < 0.1 || cpu > 32) {
        toast.error("CPUs must be between 0.1 and 32")
        return
      }
      const ttlVal = ttl === "" ? null : parseInt(ttl)
      if (ttlVal !== null && (isNaN(ttlVal) || ttlVal < 1 || ttlVal > 720)) {
        toast.error("TTL must be between 1 and 720 hours")
        return
      }
      await onSave({
        container_memory_mb: mem,
        container_cpus: cpu,
        container_ttl_hours: ttlVal,
      })
      toast.success("Container config updated")
    } catch (err: any) {
      toast.error(err.message || "Failed to update config")
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Resource limits for the crew container. Leave defaults unless you have specific requirements.
        Changes take effect on next container start.
      </p>
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor="memory" className="text-xs flex items-center gap-1.5">
              <HardDrive className="h-3 w-3 text-muted-foreground" />
              Memory (MB)
            </Label>
            <Input
              id="memory"
              type="number"
              value={mem}
              onChange={(e) => setMem(parseInt(e.target.value) || 0)}
              disabled={!canEdit}
              min={256}
              max={32768}
              step={256}
              className="h-8 text-xs"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="cpus" className="text-xs flex items-center gap-1.5">
              <Cpu className="h-3 w-3 text-muted-foreground" />
              CPUs
            </Label>
            <Input
              id="cpus"
              type="number"
              value={cpu}
              onChange={(e) => setCpu(parseFloat(e.target.value) || 0)}
              disabled={!canEdit}
              min={0.1}
              max={32}
              step={0.1}
              className="h-8 text-xs"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ttl" className="text-xs flex items-center gap-1.5">
              <Clock className="h-3 w-3 text-muted-foreground" />
              TTL (Hours)
            </Label>
            <Input
              id="ttl"
              type="number"
              value={ttl}
              onChange={(e) => setTtl(e.target.value)}
              placeholder="Never stop"
              disabled={!canEdit}
              min={1}
              max={720}
              className="h-8 text-xs"
            />
          </div>
        </div>

        {canEdit && hasChanges && (
          <Button size="sm" onClick={handleSave} disabled={saving}>
            {saving ? "Saving..." : "Save Container Config"}
          </Button>
        )}
    </div>
  )
}
