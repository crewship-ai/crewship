"use client"

import { useState } from "react"
import { motion, AnimatePresence, useReducedMotion } from "motion/react"
import { Check, ChevronDown, FlaskConical } from "lucide-react"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS, getModelLabel } from "@/lib/cli-adapters"
import { getAdapterBrand } from "@/lib/cli-adapter-brand"

const ease = [0.16, 1, 0.3, 1] as const

interface ToolchainPickerProps {
  /** The selected adapter key (e.g. "CLAUDE_CODE"). */
  value: string
  onChange: (key: string) => void
}

/**
 * The "Agent toolchain" choice on the wizard's model step.
 *
 * Only Claude Code is production-ready today — the onboarding image is
 * conformance-tested with it alone, and picking anything else blocks
 * Continue with an explanation (see canContinue in page.tsx). The picker used
 * to present all six adapters as a flat 2×3 grid of equal chips, which told a
 * first-time user the opposite: six equally good choices, five of which then
 * refused to proceed.
 *
 * So the layout says what the code enforces. Every production adapter is a
 * full card with its real brand mark and a "Fully supported" badge; the
 * experimental ones are still selectable — same keys, same `onChange` — but
 * sit behind a disclosure, each carrying its own "Experimental" badge. The
 * disclosure opens itself when the current value is experimental (a resumed
 * session, or a user who opened it and picked one) so the selection is never
 * hidden from the person who made it.
 */
export function ToolchainPicker({ value, onChange }: ToolchainPickerProps) {
  const reduce = useReducedMotion()
  const production = CLI_ADAPTER_KEYS.filter((k) => CLI_ADAPTERS[k].status === "production")
  const experimental = CLI_ADAPTER_KEYS.filter((k) => CLI_ADAPTERS[k].status !== "production")
  const valueIsExperimental = experimental.includes(value)
  const [expanded, setExpanded] = useState(valueIsExperimental)
  const open = expanded || valueIsExperimental

  return (
    <div className="space-y-2" data-testid="onboarding-toolchain-picker">
      {production.map((key) => {
        const cfg = CLI_ADAPTERS[key]
        const Icon = cfg.icon
        const brand = getAdapterBrand(key)
        const active = value === key
        return (
          <motion.button
            key={key}
            type="button"
            aria-pressed={active}
            data-testid="onboarding-toolchain-production"
            onClick={() => onChange(key)}
            whileTap={{ scale: 0.99 }}
            className={`flex w-full items-center gap-3 rounded-2xl border p-3.5 text-left transition-colors ${
              active ? "border-primary bg-primary/5" : "border-border hover:bg-muted/50"
            }`}
          >
            <span
              className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl border"
              style={{ backgroundColor: brand.bg, borderColor: brand.border }}
            >
              <Icon className="h-6 w-6" style={{ color: brand.fg }} />
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-2">
                <span className="text-sm font-semibold tracking-tight">{cfg.label}</span>
                <span className="inline-flex items-center gap-1 rounded-full border border-success/30 bg-success/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.06em] text-success">
                  <Check className="h-2.5 w-2.5" />
                  Fully supported
                </span>
              </span>
              <span className="mt-0.5 block text-xs text-muted-foreground">
                {cfg.description} · {cfg.models.length} models, default {getModelLabel(cfg.defaultModel)}
              </span>
            </span>
            {active && <Check className="h-4 w-4 shrink-0 text-primary" aria-hidden="true" />}
          </motion.button>
        )
      })}

      {experimental.length > 0 && (
        <div>
          <button
            type="button"
            aria-expanded={open}
            aria-controls="onboarding-toolchain-experimental"
            onClick={() => setExpanded((v) => !v)}
            className="inline-flex items-center gap-1.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
          >
            <FlaskConical className="h-3.5 w-3.5" />
            {open ? "Hide" : "Show"} {experimental.length} experimental toolchains
            <ChevronDown className={`h-3.5 w-3.5 transition-transform ${open ? "rotate-180" : ""}`} />
          </button>
          <AnimatePresence initial={false}>
            {open && (
              <motion.div
                id="onboarding-toolchain-experimental"
                key="experimental"
                initial={reduce ? { opacity: 0 } : { opacity: 0, height: 0 }}
                animate={{ opacity: 1, height: "auto" }}
                exit={reduce ? { opacity: 0 } : { opacity: 0, height: 0 }}
                transition={{ duration: 0.3, ease }}
                className="overflow-hidden"
              >
                <p className="pb-2 text-[11px] leading-relaxed text-muted-foreground">
                  Wired up but not yet verified end to end. Their CLI may be missing from the
                  onboarding image, so setup cannot finish on one — you can add them from the
                  dashboard later.
                </p>
                <div className="grid grid-cols-2 gap-2">
                  {experimental.map((key) => {
                    const cfg = CLI_ADAPTERS[key]
                    const Icon = cfg.icon
                    const brand = getAdapterBrand(key)
                    const active = value === key
                    return (
                      <motion.button
                        key={key}
                        type="button"
                        aria-pressed={active}
                        data-testid="onboarding-toolchain-experimental-option"
                        onClick={() => onChange(key)}
                        whileTap={{ scale: 0.98 }}
                        className={`flex items-center gap-2 rounded-xl border p-2.5 text-left transition-colors ${
                          active ? "border-primary bg-primary/5" : "border-border hover:bg-muted/50"
                        }`}
                      >
                        <span
                          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg border"
                          style={{ backgroundColor: brand.bg, borderColor: brand.border }}
                        >
                          <Icon className="h-3.5 w-3.5" style={{ color: brand.fg }} />
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-xs font-medium">{cfg.label}</span>
                          <span className="block text-[10px] uppercase tracking-[0.06em] text-warn">
                            Experimental
                          </span>
                        </span>
                      </motion.button>
                    )
                  })}
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      )}
    </div>
  )
}
