"use client"

import { motion, AnimatePresence } from "motion/react"
import { Sparkles } from "lucide-react"

import { spring } from "@/lib/motion"
import { AskRail } from "../asks/ask-rail"
import type { AskForm } from "../asks/types"

/** Follow-ups have always shown at most three chips; forms join the same
 *  three, and the remainder collapses into `+N` (PRD §5.1). */
const FOLLOW_UP_LIMIT = 3

interface FollowUpsProps {
  prompts: string[]
  onPick: (text: string) => void
  show: boolean
  /** This agent's questionnaire forms. Empty for every agent that has none,
   *  which is the overwhelming majority — and with an empty list this renders
   *  exactly the three chips it always did. */
  forms?: AskForm[]
  onPickForm?: (form: AskForm) => void
  /** #2121 — greys out the chips (rather than unmounting them via `show`)
   *  while a click on one of them is still creating the session. Visible
   *  state, not a silent drop: the same rail comes back live the instant the
   *  create settles. */
  disabled?: boolean
}

export function FollowUps({ prompts, onPick, show, forms, onPickForm, disabled }: FollowUpsProps) {
  const formList = onPickForm ? forms ?? [] : []
  return (
    <AnimatePresence>
      {show && (prompts.length > 0 || formList.length > 0) && (
        <motion.div
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: 8 }}
          transition={spring.smooth}
          className="flex items-center gap-2 px-4 md:px-6 pt-1 pb-2 shrink-0"
        >
          <Sparkles className="h-3 w-3 text-muted-foreground" />
          <AskRail
            className="min-w-0 flex-1"
            questions={prompts}
            forms={formList}
            limit={FOLLOW_UP_LIMIT}
            onPickQuestion={onPick}
            onPickForm={onPickForm ?? noop}
            animateChips
            disabled={disabled}
          />
        </motion.div>
      )}
    </AnimatePresence>
  )
}

function noop() {}
