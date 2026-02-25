"use client"

import { useState, type FormEvent } from "react"
import { AlertTriangle, Save, Loader2, Check } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { AvatarPicker } from "@/components/avatar-picker"
import { CrewIconPicker } from "@/components/crew-icon-picker"
import { AVATAR_STYLES } from "@/lib/agent-avatar"

interface CrewEditFormProps {
  name: string
  description: string
  iconSeed: string
  avatarStyle: string
  agentCount: number
  saving: boolean
  crewId: string
  workspaceId: string
  onNameChange: (v: string) => void
  onDescriptionChange: (v: string) => void
  onIconSeedChange: (v: string) => void
  onAvatarStyleChange: (v: string) => void
  onSubmit: (e: FormEvent) => void
  onAgentsRefresh: () => void
}

export function CrewEditForm({
  name,
  description,
  iconSeed,
  avatarStyle,
  agentCount,
  saving,
  crewId,
  workspaceId,
  onNameChange,
  onDescriptionChange,
  onIconSeedChange,
  onAvatarStyleChange,
  onSubmit,
  onAgentsRefresh,
}: CrewEditFormProps) {
  const [applying, setApplying] = useState(false)
  const [applyResult, setApplyResult] = useState<string | null>(null)

  async function handleApplyToAll() {
    if (!avatarStyle) return
    const label = AVATAR_STYLES[avatarStyle]?.label ?? avatarStyle
    if (!confirm(
      `Apply "${label}" avatar style to all ${agentCount} agent${agentCount !== 1 ? "s" : ""} in this crew?\n\nThis will overwrite any individually set avatar styles and seeds.`
    )) return

    setApplying(true)
    setApplyResult(null)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/apply-avatar-style?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ avatar_style: avatarStyle }),
        }
      )
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed" }))
        setApplyResult(`Error: ${data.error ?? "Unknown error"}`)
      } else {
        const data = await res.json()
        setApplyResult(`Applied to ${data.updated} agent${data.updated !== 1 ? "s" : ""}`)
        onAgentsRefresh()
      }
    } catch {
      setApplyResult("Network error")
    } finally {
      setApplying(false)
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {/* Left column — General info + Crew Icon */}
        <div className="space-y-4">
          <Card>
            <CardContent className="pt-5 space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="crew-name" className="text-xs font-medium">Name</Label>
                <Input id="crew-name" value={name} onChange={(e) => onNameChange(e.target.value)} />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="crew-desc" className="text-xs font-medium">Description</Label>
                <Textarea
                  id="crew-desc"
                  value={description}
                  onChange={(e) => onDescriptionChange(e.target.value)}
                  rows={3}
                  placeholder="What does this crew do?"
                />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardContent className="pt-5 space-y-3">
              <div>
                <Label className="text-xs font-medium">Crew Icon</Label>
                <p className="text-[11px] text-muted-foreground mt-0.5">
                  Search by category or icon name
                </p>
              </div>
              <CrewIconPicker selected={iconSeed} onSelect={onIconSeedChange} />
            </CardContent>
          </Card>
        </div>

        {/* Right column — Avatar Style + Apply */}
        <div className="space-y-4">
          <Card>
            <CardContent className="pt-5 space-y-3">
              <div>
                <Label className="text-xs font-medium">Agent Avatar Style</Label>
                <p className="text-[11px] text-muted-foreground mt-0.5">
                  Default avatar style for agents in this crew
                </p>
              </div>
              <AvatarPicker
                seed={name || "preview"}
                style={avatarStyle}
                onSeedChange={() => {}}
                onStyleChange={onAvatarStyleChange}
                styleOnly
              />
            </CardContent>
          </Card>

          {avatarStyle && agentCount > 0 && (
            <Card className="border-amber-200 dark:border-amber-900">
              <CardContent className="pt-5">
                <div className="flex items-start gap-2.5">
                  <AlertTriangle className="h-4 w-4 text-amber-500 shrink-0 mt-0.5" />
                  <div className="flex-1 space-y-2">
                    <p className="text-xs text-muted-foreground leading-relaxed">
                      Override individual agent avatars with <span className="font-medium text-foreground">{AVATAR_STYLES[avatarStyle]?.label ?? avatarStyle}</span> for
                      all {agentCount} agent{agentCount !== 1 ? "s" : ""}.
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="h-7 text-xs border-amber-300 text-amber-700 hover:bg-amber-50 dark:border-amber-800 dark:text-amber-300 dark:hover:bg-amber-950/30"
                      disabled={applying}
                      onClick={handleApplyToAll}
                    >
                      {applying ? (
                        <><Loader2 className="mr-1.5 h-3 w-3 animate-spin" />Applying...</>
                      ) : (
                        `Apply to all ${agentCount} agents`
                      )}
                    </Button>
                    {applyResult && (
                      <p className={`text-[11px] ${applyResult.startsWith("Error") ? "text-destructive" : "text-emerald-600"}`}>
                        {applyResult.startsWith("Error") ? applyResult : <><Check className="inline h-3 w-3 mr-0.5" />{applyResult}</>}
                      </p>
                    )}
                  </div>
                </div>
              </CardContent>
            </Card>
          )}
        </div>
      </div>

      {/* Save — full width below */}
      <div className="mt-4 flex justify-end">
        <Button type="submit" disabled={saving} size="sm">
          {saving ? (
            <><Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />Saving...</>
          ) : (
            <><Save className="mr-1.5 h-3.5 w-3.5" />Save Changes</>
          )}
        </Button>
      </div>
    </form>
  )
}
