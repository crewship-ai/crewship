import type { CSSProperties } from "react"
import { Box, Orbit } from "lucide-react"
import type { IconType } from "react-icons"
import { SiApple, SiContainerd, SiDocker, SiPodman, SiRancher } from "react-icons/si"

import { cn } from "@/lib/utils"

// Brand identity for the container runtimes Crewship can detect (#1690).
//
// Until this existed, every runtime rendered as the same generic capitalised
// string beside the same green tick — "Orbstack", "Rancher", "Apple" — which
// is how a machine running OrbStack could be described as running Docker
// without anything looking wrong.
//
// Two rules the table below follows, and neither is negotiable:
//
//  1. **A mark or nothing.** Docker, Podman, Apple, containerd and Rancher have
//     official Simple Icons entries. Colima and OrbStack do not. Drawing an
//     approximation of a logo we do not have, or borrowing a neighbouring
//     product's, is worse than a neutral glyph — it is confidently wrong, which
//     is the exact failure this whole surface exists to fix. Those two get a
//     lucide glyph in the theme's own muted foreground, no invented colour.
//
//  2. **Both themes, always.** A brand colour that vanishes on one background
//     is worse than a neutral glyph. Apple's mark is pure black and disappears
//     on the dark console; containerd's grey and Rancher's navy go muddy there;
//     Docker's whale blue is only 3.02:1 on the light card, i.e. sitting on the
//     WCAG 1.4.11 threshold. Each official mark therefore carries a colour per
//     theme, both drawn from the vendor's own palette, and
//     runtime-icons.test.tsx holds every one of them to 3:1 against the card it
//     is drawn on.

export interface RuntimeBrand {
  /** The product's name as its vendor writes it. */
  label: string
  Icon: IconType | typeof Box
  /**
   * True when Icon is the product's real mark. False means a neutral glyph
   * stands in because no official mark exists — the colours are then unused.
   */
  official: boolean
  /** Brand colour on a light background. */
  light: string
  /** The same brand, legible on a dark background. */
  dark: string
}

const NEUTRAL = { light: "", dark: "" }

export const RUNTIME_BRANDS: Record<string, RuntimeBrand> = {
  // Both are Docker's own blues: #1D63ED is the current brand primary and
  // holds up on white; #2496ED is the whale blue Simple Icons ships, which is
  // brighter and belongs on the dark card.
  docker: { label: "Docker", Icon: SiDocker, official: true, light: "#1D63ED", dark: "#2496ED" },
  podman: { label: "Podman", Icon: SiPodman, official: true, light: "#892CA0", dark: "#C069DA" },
  // Apple ships its mark black on light and white on dark. Anything else is
  // not the Apple mark.
  apple: { label: "Apple Containers", Icon: SiApple, official: true, light: "#000000", dark: "#FFFFFF" },
  rancher: { label: "Rancher Desktop", Icon: SiRancher, official: true, light: "#0075A8", dark: "#2EA8DC" },
  containerd: { label: "containerd", Icon: SiContainerd, official: true, light: "#575757", dark: "#A3A3A3" },
  // The detector's label for a containerd endpoint is `nerdctl` — the client,
  // not the daemon. Same product, same mark. (It can no longer answer at all,
  // see #1687, but a label the code can still emit gets a brand.)
  nerdctl: { label: "containerd", Icon: SiContainerd, official: true, light: "#575757", dark: "#A3A3A3" },
  // No Simple Icons entry for either of these. Neutral glyphs, chosen to be
  // distinguishable from one another rather than evocative of a logo.
  colima: { label: "Colima", Icon: Box, official: false, ...NEUTRAL },
  orbstack: { label: "OrbStack", Icon: Orbit, official: false, ...NEUTRAL },
}

/**
 * runtimeBrand resolves a runtime key from the API to its brand identity.
 *
 * An unrecognised key keeps its own name and takes the neutral glyph: the
 * server knows about a runtime this build does not, and printing what it said
 * beats printing "Unknown".
 */
export function runtimeBrand(runtime: string): RuntimeBrand {
  return RUNTIME_BRANDS[runtime] ?? { label: runtime, Icon: Box, official: false, ...NEUTRAL }
}

/**
 * RuntimeIcon renders a runtime's mark in its brand colour, in either theme.
 *
 * The colour arrives as two CSS custom properties rather than a single inline
 * `color`, because an inline colour cannot respond to the theme — the light
 * variant would follow the user into dark mode and, for Apple, become an
 * invisible black square on a black card.
 */
export function RuntimeIcon({
  runtime,
  className,
}: {
  runtime: string
  className?: string
}) {
  const brand = runtimeBrand(runtime)
  const Icon = brand.Icon

  if (!brand.official) {
    return (
      <Icon
        aria-hidden
        data-runtime-icon={runtime}
        data-brand-mark="none"
        className={cn("text-muted-foreground", className)}
      />
    )
  }

  return (
    <Icon
      aria-hidden
      data-runtime-icon={runtime}
      data-brand-mark="official"
      className={cn(
        "text-[color:var(--rt-brand-light)] dark:text-[color:var(--rt-brand-dark)]",
        className,
      )}
      style={
        {
          "--rt-brand-light": brand.light,
          "--rt-brand-dark": brand.dark,
        } as CSSProperties
      }
    />
  )
}
