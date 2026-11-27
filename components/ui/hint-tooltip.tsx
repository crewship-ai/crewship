"use client"

import * as React from "react"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./tooltip"

/**
 * One sentence that explains a control or a state, shown on hover and on
 * keyboard focus. The child is the trigger: a control, or an element that is
 * focusable itself or holds the focusable control (focus bubbles to it). It
 * keeps its own name, role and description; the hint only adds to them.
 * Carries its own provider, so it works wherever it is mounted.
 */
export function HintTooltip({ hint, children, side = "bottom" }: { hint: string; children: React.ReactElement; side?: "top" | "right" | "bottom" | "left" }) {
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>{children}</TooltipTrigger>
        <TooltipContent side={side} className="max-w-xs text-label">{hint}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
