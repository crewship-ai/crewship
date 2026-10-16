"use client"

import * as React from "react"
import { Check } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * The step bar every multi-step dialog uses (Harbor 2 kit, #3366): numbered
 * steps joined by short rules, finished steps green with a check, the current
 * one raised in the accent. A step before the current one — or any step when
 * `freeNavigation` is set — can be clicked to go back to it. On a phone the
 * labels give way to "2 / 5".
 */
export function WizardSteps({
  steps,
  current,
  onStepChange,
  freeNavigation = false,
  className,
}: {
  steps: string[]
  /** 0-based index of the step on screen. */
  current: number
  onStepChange?: (index: number) => void
  /** Allow jumping forward too (every step is independently valid). */
  freeNavigation?: boolean
  className?: string
}) {
  return (
    <nav aria-label="Steps" className={cn("wizard-steps", className)}>
      <ol>
        {steps.map((label, i) => {
          const state = i < current ? "done" : i === current ? "current" : "todo"
          const clickable = onStepChange != null && i !== current && (freeNavigation || i < current)
          const body = (
            <>
              <span className="wizard-steps-n" aria-hidden>{state === "done" ? <Check /> : i + 1}</span>
              <span className="wizard-steps-label">{label}</span>
            </>
          )
          return (
            <li key={label} data-state={state}>
              {i > 0 && <span className="wizard-steps-rule" aria-hidden />}
              {clickable ? (
                <button type="button" className="wizard-steps-item" onClick={() => onStepChange(i)} aria-label={`Step ${i + 1}: ${label}`}>
                  {body}
                </button>
              ) : (
                <span className="wizard-steps-item" aria-current={state === "current" ? "step" : undefined}>
                  {body}
                </span>
              )}
            </li>
          )
        })}
      </ol>
      <span className="wizard-steps-count" aria-hidden>{current + 1} / {steps.length}</span>
    </nav>
  )
}
