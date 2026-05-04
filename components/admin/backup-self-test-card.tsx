"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { CheckCircle2, Loader2, PlayCircle, XCircle } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { useBackupSelfTest, type SelfTestResponse } from "@/hooks/use-backups"
import { cn } from "@/lib/utils"

interface SelfTestCardProps {
  workspaceId: string | undefined
}

function formatRelative(t: number | null): string {
  if (!t) return "never"
  const delta = Math.max(0, Date.now() - t)
  const min = Math.floor(delta / 60_000)
  if (min < 1) return "just now"
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const d = Math.floor(hr / 24)
  return `${d}d ago`
}

/**
 * End-to-end backup pipeline canary. Backend creates a throwaway test
 * workspace, runs the full create → destroy → restore → verify cycle
 * against it, and reports per-step pass/fail. Safe to run any time —
 * the canary workspace is isolated from real data.
 *
 * Per Supabase's backup playbook the recommendation is to exercise
 * this quarterly (otherwise you find out backups are broken when you
 * actually need to restore one). This card surfaces the last-run
 * timestamp prominently so an operator can see at a glance whether the
 * pipeline has been exercised recently.
 */
export function BackupSelfTestCard({ workspaceId }: SelfTestCardProps) {
  const test = useBackupSelfTest(workspaceId)
  // Local state for "ran in this session" — the backend doesn't yet
  // persist last-run timestamp anywhere queryable, so we cache it
  // session-local. Future: surface a server-side "last canary run"
  // field on the metrics endpoint.
  const [lastResult, setLastResult] = useState<SelfTestResponse | null>(null)
  const [lastRanAt, setLastRanAt] = useState<number | null>(null)

  async function run() {
    try {
      const result = await test.mutateAsync()
      setLastResult(result)
      setLastRanAt(Date.now())
      if (result.ok) {
        toast.success(`Self-test passed in ${result.duration_ms}ms`)
      } else {
        toast.error(
          `Self-test failed`,
          { description: `${result.steps.filter((s) => !s.ok).length} step(s) failed` },
        )
      }
    } catch (err) {
      toast.error(
        "Self-test could not run",
        { description: err instanceof Error ? err.message : "Unknown error" },
      )
    }
  }

  return (
    <div className="rounded-lg border bg-card overflow-hidden">
      <div className="px-4 py-2.5 border-b border-border/60 flex items-center gap-3">
        <PlayCircle className="h-3.5 w-3.5 text-muted-foreground" />
        <h3 className="text-sm font-semibold">Backup self-test</h3>
        <span className="ml-auto text-xs text-muted-foreground">
          Canary round-trip — recommended quarterly
        </span>
      </div>

      <div className="p-4 space-y-3">
        <div className="flex items-start gap-3">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">Last run:</span>
              {!lastResult ? (
                <span className="text-muted-foreground">never</span>
              ) : lastResult.ok ? (
                <span className="inline-flex items-center gap-1 text-emerald-500">
                  <CheckCircle2 className="h-3.5 w-3.5" /> passed
                </span>
              ) : (
                <span className="inline-flex items-center gap-1 text-destructive">
                  <XCircle className="h-3.5 w-3.5" /> failed
                </span>
              )}
              {lastResult ? (
                <span className="text-muted-foreground font-mono text-xs">
                  · {formatRelative(lastRanAt)} · {lastResult.duration_ms}ms
                  {lastResult.trace_id ? ` · trace ${lastResult.trace_id.slice(0, 12)}…` : ""}
                </span>
              ) : null}
            </div>
            <p className="text-[11px] text-muted-foreground mt-1.5">
              Verifies the full create → destroy → restore → verify pipeline against an
              isolated test workspace. No real data is touched.
            </p>
          </div>
          <Button size="sm" disabled={test.isPending} onClick={run}>
            {test.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" />
            ) : (
              <PlayCircle className="h-3.5 w-3.5 mr-1" />
            )}
            {test.isPending ? "Running…" : "Run self-test"}
          </Button>
        </div>

        <AnimatePresence>
          {lastResult && (
            <motion.div
              key={lastRanAt}
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              exit={{ opacity: 0, height: 0 }}
              transition={{ duration: 0.2, ease: "easeOut" }}
              className="overflow-hidden"
            >
              <div className="rounded-md border border-border/60 bg-muted/30 p-3">
                <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground mb-2">
                  Pipeline steps
                </div>
                <ul className="space-y-1 font-mono text-[11px]">
                  {lastResult.steps.map((step, i) => (
                    <motion.li
                      key={`${step.name}-${i}`}
                      initial={{ opacity: 0, x: -4 }}
                      animate={{ opacity: 1, x: 0 }}
                      transition={{ duration: 0.15, delay: i * 0.04, ease: "easeOut" }}
                      className={cn(
                        "flex items-center gap-2",
                        step.ok ? "text-foreground/80" : "text-destructive",
                      )}
                    >
                      {step.ok ? (
                        <CheckCircle2 className="h-3 w-3 text-emerald-500 shrink-0" />
                      ) : (
                        <XCircle className="h-3 w-3 text-destructive shrink-0" />
                      )}
                      <span className="flex-1 truncate">{step.name}</span>
                      <span className="text-muted-foreground">
                        {step.duration_ms}ms
                      </span>
                      {step.error ? (
                        <span
                          className="text-destructive truncate max-w-[300px]"
                          title={step.error}
                        >
                          {step.error}
                        </span>
                      ) : null}
                    </motion.li>
                  ))}
                </ul>
              </div>
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  )
}
