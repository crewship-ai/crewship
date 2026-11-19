"use client"

import * as React from "react"
import { Eye, Minus, Send, Upload, type LucideIcon } from "lucide-react"

import { Segmented } from "@/components/ui/segmented"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"

import { LEVELS, LEVEL_LABEL, WORK_LEVELS, type Level } from "./crew-links-model"

export const LEVEL_TONE: Record<Level, StatusTone> = { off: "muted", work: "blue", view: "success", deliver: "warn" }
export const LEVEL_ICON: Record<Level, LucideIcon> = { off: Minus, work: Send, view: Eye, deliver: Upload }

/** A level as a quiet pill: the only thing a reader below Manager sees. */
export function LevelTag({ level, className }: { level: Level; className?: string }) {
  return <StatusPill tone={LEVEL_TONE[level]} label={LEVEL_LABEL[level]} className={className} />
}

/** The pill for a pair the row marks inactive: muted, and its levels read as Off. */
export function InactiveTag() {
  return <StatusPill tone="muted" label="Inactive" title="This link is switched off. Its levels show as Off." />
}

/**
 * The ladder for one direction: Off, Work only, View files, Deliver files.
 * Each step includes the ones before it. A server that does not report file
 * access gets only the first two steps.
 */
export function LevelLadder({ value, onChange, ariaLabel, disabled, filesKnown = true }: {
  value: Level
  onChange: (level: Level) => void
  /** "Support to Ops": which direction this is. */
  ariaLabel: string
  disabled?: boolean
  filesKnown?: boolean
}) {
  const steps = filesKnown ? LEVELS : WORK_LEVELS
  return (
    <div className="max-w-full" data-slot="level-ladder" aria-disabled={disabled || undefined}>
      <Segmented<Level>
        ariaLabel={ariaLabel}
        value={value}
        onChange={(next) => { if (!disabled && next !== value) onChange(next) }}
        options={steps.map((l) => ({ value: l, label: LEVEL_LABEL[l], disabled }))}
        className="max-w-full flex-wrap"
      />
    </div>
  )
}
