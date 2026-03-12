"use client"

import { useState, useMemo } from "react"
import { RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { AVATAR_STYLES, DEFAULT_AVATAR_STYLE, getAgentAvatarUrl } from "@/lib/agent-avatar"

interface AvatarPickerProps {
  seed: string
  style: string
  onSeedChange: (seed: string) => void
  onStyleChange: (style: string) => void
  lockedStyle?: string | null
  styleOnly?: boolean
}

function randomSeed(): string {
  return Math.random().toString(36).substring(2, 10)
}

export function AvatarPicker({ seed, style, onSeedChange, onStyleChange, lockedStyle, styleOnly }: AvatarPickerProps) {
  const [previewSeeds] = useState(() =>
    Array.from({ length: 8 }, () => randomSeed())
  )

  const effectiveStyle = lockedStyle || style || DEFAULT_AVATAR_STYLE
  const styleKeys = Object.keys(AVATAR_STYLES)

  const currentUrl = useMemo(
    () => getAgentAvatarUrl(seed, effectiveStyle),
    [seed, effectiveStyle],
  )

  return (
    <div className="space-y-4">
      {!styleOnly && (
        <div className="flex items-start gap-4">
          <img
            src={currentUrl}
            alt="Current avatar"
            className="h-20 w-20 rounded-2xl border shrink-0"
          />
          <div className="space-y-2 flex-1">
            <Label htmlFor="avatar-seed">Avatar Seed</Label>
            <div className="flex gap-2">
              <Input
                id="avatar-seed"
                value={seed}
                onChange={(e) => onSeedChange(e.target.value)}
                placeholder="Agent name or custom text"
                className="font-mono text-xs"
              />
              <Button
                type="button"
                variant="outline"
                size="icon"
                onClick={() => onSeedChange(randomSeed())}
                title="Randomize"
              >
                <RefreshCw className="h-3.5 w-3.5" />
              </Button>
            </div>
            <p className="text-micro text-muted-foreground">
              Different seeds produce different faces. Leave empty to use agent name.
            </p>
          </div>
        </div>
      )}

      {!lockedStyle && (
        <div className="space-y-2">
          {!styleOnly && <Label>Avatar Style</Label>}
          <div className="grid grid-cols-5 gap-2">
            {styleKeys.map((key) => {
              const entry = AVATAR_STYLES[key]
              const isActive = effectiveStyle === key
              return (
                <button
                  key={key}
                  type="button"
                  onClick={() => onStyleChange(key)}
                  className={`flex flex-col items-center gap-1.5 rounded-lg border p-2 transition-colors ${
                    isActive ? "border-primary bg-primary/5 ring-1 ring-primary" : "border-border hover:bg-muted"
                  }`}
                >
                  <img
                    src={getAgentAvatarUrl(seed || "preview", key)}
                    alt={entry.label}
                    className="h-10 w-10 rounded-lg"
                  />
                  <span className="text-micro text-muted-foreground leading-tight text-center">
                    {entry.label}
                  </span>
                </button>
              )
            })}
          </div>
          {!styleOnly && (
            <p className="text-micro text-muted-foreground">
              Crew can override style for all its agents. Agent-level style takes priority.
            </p>
          )}
        </div>
      )}

      {lockedStyle && (
        <p className="text-micro text-muted-foreground">
          Style is set by crew template ({AVATAR_STYLES[lockedStyle]?.label ?? lockedStyle}). Change it in crew settings.
        </p>
      )}

      {!styleOnly && (
        <div className="space-y-2">
          <Label>Quick Pick</Label>
          <div className="grid grid-cols-8 gap-2">
            {previewSeeds.map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => onSeedChange(s)}
                className={`rounded-lg border p-1.5 transition-colors hover:bg-muted ${
                  seed === s ? "border-primary bg-primary/5" : "border-border"
                }`}
              >
                <img
                  src={getAgentAvatarUrl(s, effectiveStyle)}
                  alt={s}
                  className="h-8 w-8 rounded"
                />
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
