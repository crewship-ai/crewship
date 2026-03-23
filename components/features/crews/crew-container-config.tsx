"use client"

import { useEffect, useState } from "react"
import { Settings2, HardDrive, Cpu, Clock } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
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
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          <Settings2 className="h-4 w-4 text-primary" />
          <CardTitle className="text-base">Container Configuration</CardTitle>
        </div>
        <CardDescription>
          Resource limits and lifecycle policy for the crew runtime.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
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

        <p className="text-[11px] text-muted-foreground">
          Container will auto-stop after specified hours of inactivity. Memory and CPU changes take effect upon next container start.
        </p>

        {canEdit && hasChanges && (
          <Button size="sm" onClick={handleSave} disabled={saving}>
            {saving ? "Saving..." : "Save Container Config"}
          </Button>
        )}
      </CardContent>
    </Card>
  )
}
