"use client"

/**
 * The second crumb of the Vault's header: "Vault › Secrets ▾" (v67). The
 * space you are in is also the way to the other one, so the header and the
 * panel's two rows agree. A viewer with one space (no AI providers) gets the
 * plain word.
 */

import { ChevronDown } from "lucide-react"

import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import type { VaultSpace } from "@/lib/credentials/vault-url-state"

const SPACE_LABEL: Record<VaultSpace, string> = { secrets: "Secrets", providers: "AI providers" }

export function VaultSpaceCrumb({
  space,
  onSpace,
  chooser,
}: {
  space: VaultSpace
  onSpace: (s: VaultSpace) => void
  /** False when the viewer has only Secrets. */
  chooser: boolean
}) {
  if (!chooser) return <>{SPACE_LABEL[space]}</>
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          data-testid="vault-space-crumb"
          title="Switch space"
          className="kit-tap inline-flex items-center gap-1 rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-primary"
        >
          {SPACE_LABEL[space]}
          <ChevronDown className="size-3 text-muted-foreground-soft" aria-hidden />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-40">
        <DropdownMenuRadioGroup value={space} onValueChange={(v) => onSpace(v as VaultSpace)}>
          {(Object.keys(SPACE_LABEL) as VaultSpace[]).map((s) => (
            <DropdownMenuRadioItem key={s} value={s}>
              {SPACE_LABEL[s]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
