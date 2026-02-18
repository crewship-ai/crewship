"use client"

import type { FormEvent } from "react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"

interface CrewEditFormProps {
  name: string
  description: string
  color: string
  icon: string
  containerTtlHours: string
  containerMemoryMb: string
  containerCpus: string
  saving: boolean
  onNameChange: (v: string) => void
  onDescriptionChange: (v: string) => void
  onColorChange: (v: string) => void
  onIconChange: (v: string) => void
  onTtlChange: (v: string) => void
  onMemoryChange: (v: string) => void
  onCpusChange: (v: string) => void
  onSubmit: (e: FormEvent) => void
}

export function CrewEditForm({
  name,
  description,
  color,
  icon,
  containerTtlHours,
  containerMemoryMb,
  containerCpus,
  saving,
  onNameChange,
  onDescriptionChange,
  onColorChange,
  onIconChange,
  onTtlChange,
  onMemoryChange,
  onCpusChange,
  onSubmit,
}: CrewEditFormProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Edit Crew</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="crew-name">Name</Label>
            <Input id="crew-name" value={name} onChange={(e) => onNameChange(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="crew-desc">Description</Label>
            <Textarea id="crew-desc" value={description} onChange={(e) => onDescriptionChange(e.target.value)} rows={3} />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="crew-color">Color</Label>
              <div className="flex items-center gap-2">
                <input
                  type="color"
                  id="crew-color"
                  value={color}
                  onChange={(e) => onColorChange(e.target.value)}
                  className="h-9 w-9 rounded border cursor-pointer"
                />
                <Input
                  value={color}
                  onChange={(e) => onColorChange(e.target.value)}
                  className="flex-1 font-mono text-sm"
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="crew-icon">Icon (emoji)</Label>
              <Input
                id="crew-icon"
                value={icon}
                onChange={(e) => onIconChange(e.target.value)}
                placeholder="e.g. 🚀"
                maxLength={10}
              />
            </div>
          </div>

          <div className="grid grid-cols-3 gap-4">
            <div className="space-y-2">
              <Label htmlFor="crew-memory">Memory (MB)</Label>
              <Input
                id="crew-memory"
                type="number"
                min={512}
                max={32768}
                value={containerMemoryMb}
                onChange={(e) => onMemoryChange(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="crew-cpus">CPUs</Label>
              <Input
                id="crew-cpus"
                type="number"
                min={0.5}
                max={16}
                step={0.5}
                value={containerCpus}
                onChange={(e) => onCpusChange(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="crew-ttl">TTL (hours)</Label>
              <Input
                id="crew-ttl"
                type="number"
                min={1}
                max={720}
                placeholder="No limit (empty)"
                value={containerTtlHours}
                onChange={(e) => onTtlChange(e.target.value)}
              />
            </div>
          </div>

          <Button type="submit" disabled={saving}>
            {saving ? "Saving..." : "Save Changes"}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
