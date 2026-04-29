"use client"

import { motion, AnimatePresence } from "motion/react"
import { Sparkles } from "lucide-react"

import { Suggestion } from "@/components/ai-elements/suggestion"
import { spring, stagger } from "@/lib/motion"

interface FollowUpsProps {
  prompts: string[]
  onPick: (text: string) => void
  show: boolean
}

export function FollowUps({ prompts, onPick, show }: FollowUpsProps) {
  return (
    <AnimatePresence>
      {show && prompts.length > 0 && (
        <motion.div
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: 8 }}
          transition={spring.smooth}
          className="flex items-center gap-2 px-4 md:px-6 pt-1 pb-2 shrink-0"
        >
          <Sparkles className="h-3 w-3 text-muted-foreground" />
          <motion.div
            variants={{ show: stagger.chips, hidden: {} }}
            initial="hidden"
            animate="show"
            className="flex flex-wrap items-center gap-1.5"
          >
            {prompts.slice(0, 3).map((p) => (
              <motion.div
                key={p}
                variants={{
                  hidden: { opacity: 0, scale: 0.95, y: 4 },
                  show: { opacity: 1, scale: 1, y: 0 },
                }}
                transition={spring.snappy}
              >
                <Suggestion suggestion={p} onClick={onPick} />
              </motion.div>
            ))}
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}
