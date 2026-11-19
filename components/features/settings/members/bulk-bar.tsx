"use client"

import { useState } from "react"
import { motion } from "motion/react"
import { ChevronDown, ListChecks } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import {
  CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { selectionBar, selectionBarButton } from "@/components/features/settings/shared"
import { ALL_CAPABILITIES, Capability, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"
import { HIGH_STAKES_CAPABILITIES } from "@/components/admin/member-capabilities"

import { ACCESS_PROFILES, ASSIGNABLE_ROLES, CAPABILITY_NAME, ROLE_LABEL } from "./members-model"

/** What the dialog asks for: a preset to set everyone to, and/or single
 *  abilities to give or take away. */
export interface AbilityChange {
  preset: CapabilityBundle | null
  give: CapabilityValue[]
  take: CapabilityValue[]
}

type Step = "leave" | "give" | "take"

const SINGLE = ALL_CAPABILITIES.filter((c) => c !== Capability.Chat)

/**
 * Abilities for all selected people in one dialog: set everyone to a preset,
 * or give / take away single abilities (the old Set access, Grant and Revoke
 * menus, merged). Apply only drafts — the change lands when the page is
 * saved — and the page asks once more when it gives anything high-stakes.
 */
export function BulkAbilitiesDialog({ open, onOpenChange, count, onApply }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  count: number
  onApply: (change: AbilityChange) => void
}) {
  const [preset, setPreset] = useState<CapabilityBundle | "">("")
  const [steps, setSteps] = useState<Partial<Record<CapabilityValue, Step>>>({})
  const reset = () => { setPreset(""); setSteps({}) }
  const give = SINGLE.filter((c) => steps[c] === "give")
  const take = SINGLE.filter((c) => steps[c] === "take")
  const empty = !preset && give.length === 0 && take.length === 0
  const apply = () => {
    if (empty) return
    onApply({ preset: preset || null, give, take })
    reset()
    onOpenChange(false)
  }
  return (
    <CreateSurface open={open} size="md" dirty={!empty} onOpenChange={(o) => { if (!o) reset(); onOpenChange(o) }} onSubmit={apply}>
      <CreateSurfaceHeader icon={ListChecks} accent="blue" title={`Abilities for ${count} ${count === 1 ? "person" : "people"}`}
        description="Takes effect when you save the page." onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody className="space-y-4">
        <div className="space-y-1.5">
          <span className="text-label font-medium">Set everyone to</span>
          <div>
            <Segmented ariaLabel="Set everyone to" value={preset || "leave"} onChange={(v) => setPreset(v === "leave" ? "" : (v as CapabilityBundle))}
              options={[{ value: "leave", label: "Leave as is" }, ...ACCESS_PROFILES.map((p) => ({ value: p.key, label: p.label, title: p.summary }))]} />
          </div>
        </div>
        <div className="space-y-1.5">
          <span className="text-label font-medium">Or change single abilities</span>
          <ul className="overflow-hidden rounded-card border border-border">
            {SINGLE.map((cap) => (
              <li key={cap} className="flex flex-wrap items-center justify-between gap-2 border-b border-border/60 px-3 py-2 last:border-b-0">
                <span className="text-control">
                  {CAPABILITY_NAME[cap]}
                  {HIGH_STAKES_CAPABILITIES.has(cap) && <span className="ml-1.5 text-micro font-medium text-destructive">high-stakes</span>}
                </span>
                <Segmented ariaLabel={CAPABILITY_NAME[cap]} size="xs" value={steps[cap] ?? "leave"}
                  onChange={(v) => setSteps((cur) => ({ ...cur, [cap]: v as Step }))}
                  options={[{ value: "leave", label: "Leave" }, { value: "give", label: "Give" }, { value: "take", label: "Take away" }]} />
              </li>
            ))}
          </ul>
          <p className="text-label text-muted-foreground">Giving a high-stakes ability asks once more. Owners and you yourself are skipped.</p>
        </div>
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel="Apply" onPrimary={apply} primaryDisabled={empty} />
    </CreateSurface>
  )
}

/**
 * What to do with the ticked rows: a bar floating over the bottom of the
 * page, like a mail client's selection bar. Role changes one rung at a time
 * (a menu, then the page's confirm); Abilities opens the dialog above.
 */
export function BulkBar({ count, busy, lifted, canChangeRole, callerRole, onAbilities, onRole, onClear, reduce }: {
  count: number
  busy: boolean
  /** The save bar is showing: sit above it, not on it. */
  lifted: boolean
  canChangeRole: boolean
  callerRole?: string | null
  onAbilities: () => void
  onRole: (r: string) => void
  onClear: () => void
  reduce: boolean
}) {
  const callerRank = ASSIGNABLE_ROLES.indexOf(callerRole as (typeof ASSIGNABLE_ROLES)[number])
  const roles = callerRole === "OWNER" ? [...ASSIGNABLE_ROLES] : ASSIGNABLE_ROLES.filter((_, i) => (callerRank < 0 ? false : i < callerRank))
  return (
    <motion.div role="toolbar" aria-label="Change the selected people" data-slot="members-bulk"
      initial={reduce ? false : { y: 24, opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={reduce ? undefined : { y: 24, opacity: 0 }} transition={{ duration: 0.18 }}
      className={cn(selectionBar, "fixed left-1/2 -translate-x-1/2 transition-[bottom]", lifted ? "bottom-20" : "bottom-6")}>
      <span className="px-1 text-xs"><span className="font-semibold tabular-nums">{count}</span> selected</span>
      {canChangeRole && roles.length > 0 && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild disabled={busy}>
            <Button variant="outline" size="sm" className={selectionBarButton}>Role<ChevronDown className="h-3 w-3" /></Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" side="top" className="w-56">
            <DropdownMenuLabel className="text-micro font-semibold text-primary-hover">Change role</DropdownMenuLabel>
            {roles.map((r) => <DropdownMenuItem key={r} onSelect={() => onRole(r)} className="text-xs">{ROLE_LABEL[r]}</DropdownMenuItem>)}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      <Button variant="outline" size="sm" className={selectionBarButton} disabled={busy} onClick={onAbilities}>Abilities</Button>
      <Button variant="ghost" size="sm" className={cn(selectionBarButton, "border-transparent px-2.5")} onClick={onClear}>Clear</Button>
    </motion.div>
  )
}
