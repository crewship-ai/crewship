"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Check, ChevronsUpDown, CheckCircle2, PlayCircle, XCircle } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useBackupSelfTest, useCrewsForBackup, type SelfTestResponse } from "@/hooks/use-backups"
import { cn } from "@/lib/utils"

interface SelfTestCardProps {
  workspaceId: string | undefined
}

function formatBytes(n: number): string {
  if (!n || n < 1024) return `${n ?? 0} B`
  const units = ["KB", "MB", "GB", "TB"]
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
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
 * Per-crew backup pipeline canary. Backend writes a sentinel file
 * inside the named crew's container, snapshots through the full
 * collect → mutate → restore → verify loop, and reports bit-identical
 * outcome. Quick (~50ms on a small crew) and safe — the canary file
 * is the only filesystem mutation.
 *
 * This card intentionally requires a crew pick rather than auto-
 * selecting the first running crew: operators want to choose WHICH
 * crew they're certifying, and selecting silently would mean they
 * never know which one passed/failed historically.
 *
 * Un-provisioned crews return ok=false with error="container not
 * found" — the card shows that as a normal failure outcome with the
 * exact message so the operator knows to provision before retrying.
 */
export function BackupSelfTestCard({ workspaceId }: SelfTestCardProps) {
  const test = useBackupSelfTest(workspaceId)
  const crewsQuery = useCrewsForBackup(workspaceId)

  const [crewId, setCrewId] = useState("")
  const [pickerOpen, setPickerOpen] = useState(false)
  const [lastResult, setLastResult] = useState<SelfTestResponse | null>(null)
  const [lastRanAt, setLastRanAt] = useState<number | null>(null)

  const selectedCrew = crewsQuery.data?.find((c) => c.id === crewId)

  async function run() {
    if (!crewId) {
      toast.error("Pick a crew to self-test first")
      return
    }
    try {
      const result = await test.mutateAsync({ crew_id: crewId })
      setLastResult(result)
      setLastRanAt(Date.now())
      if (result.ok) {
        toast.success(`Self-test passed in ${result.elapsed_ms}ms`, {
          description: `${result.crew_slug} · canary ${result.canary_bytes}B → bundle ${formatBytes(result.bundle_bytes)}`,
        })
      } else {
        toast.error("Self-test failed", { description: result.error || "no detail provided" })
      }
    } catch (err) {
      toast.error("Self-test could not run", {
        description: err instanceof Error ? err.message : "Unknown error",
      })
    }
  }

  return (
    <div className="rounded-lg border bg-card overflow-hidden">
      <div className="px-4 py-2.5 border-b border-border/60 flex items-center gap-3">
        <PlayCircle className="h-3.5 w-3.5 text-muted-foreground" />
        <h3 className="text-sm font-semibold">Backup self-test</h3>
        <span className="ml-auto text-xs text-muted-foreground">
          Per-crew canary round-trip — recommended quarterly
        </span>
      </div>

      <div className="p-4 space-y-3">
        <div className="grid grid-cols-1 md:grid-cols-[1fr_auto] gap-3 items-end">
          <div className="space-y-1.5">
            <label
              htmlFor="self-test-crew"
              className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground"
            >
              Crew to test
            </label>
            <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
              <PopoverTrigger asChild>
                <Button
                  id="self-test-crew"
                  type="button"
                  variant="outline"
                  role="combobox"
                  aria-expanded={pickerOpen}
                  className="w-full justify-between font-normal h-9"
                  disabled={crewsQuery.isLoading}
                >
                  {crewsQuery.isLoading ? (
                    <span className="flex items-center gap-2 text-muted-foreground">
                      <Spinner className="h-3.5 w-3.5" />
                      Loading crews…
                    </span>
                  ) : selectedCrew ? (
                    <span className="flex items-center gap-2 min-w-0">
                      <span className="truncate">{selectedCrew.name}</span>
                      <span className="font-mono text-xs text-muted-foreground truncate">
                        {selectedCrew.slug}
                      </span>
                    </span>
                  ) : (
                    <span className="text-muted-foreground">Pick a crew…</span>
                  )}
                  <ChevronsUpDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-50" />
                </Button>
              </PopoverTrigger>
              <PopoverContent className="p-0 w-[--radix-popover-trigger-width]" align="start">
                <Command>
                  <CommandInput placeholder="Search crews…" />
                  <CommandList>
                    <CommandEmpty>
                      {crewsQuery.isError ? "Failed to load crews" : "No crews match"}
                    </CommandEmpty>
                    <CommandGroup>
                      {(crewsQuery.data ?? []).map((c) => (
                        <CommandItem
                          key={c.id}
                          value={`${c.name} ${c.slug} ${c.id}`}
                          onSelect={() => {
                            setCrewId(c.id)
                            setPickerOpen(false)
                          }}
                          className="flex items-center gap-2"
                        >
                          <Check
                            className={cn(
                              "h-3.5 w-3.5",
                              c.id === crewId ? "opacity-100" : "opacity-0",
                            )}
                          />
                          <span className="flex-1 truncate">{c.name}</span>
                          <span className="font-mono text-xs text-muted-foreground">{c.slug}</span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
          </div>
          <Button size="sm" disabled={!crewId || test.isPending} onClick={run} className="h-9">
            {test.isPending ? (
              <Spinner className="h-3.5 w-3.5 mr-1" />
            ) : (
              <PlayCircle className="h-3.5 w-3.5 mr-1" />
            )}
            {test.isPending ? "Running…" : "Run self-test"}
          </Button>
        </div>

        <p className="text-[11px] text-muted-foreground">
          Verifies the full create → mutate → restore → verify pipeline against the chosen
          crew's container. The canary file is the only mutation; safe to run on a busy
          crew. Un-provisioned crews return a clean failure rather than crashing.
        </p>

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
              <div
                className={cn(
                  "rounded-md border p-3 text-xs",
                  lastResult.ok
                    ? "bg-emerald-500/5 border-emerald-500/30"
                    : "bg-destructive/5 border-destructive/30",
                )}
              >
                <div className="flex items-center gap-2 mb-2">
                  {lastResult.ok ? (
                    <>
                      <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
                      <span className="font-semibold text-emerald-500 uppercase tracking-wider text-[10px]">
                        passed
                      </span>
                    </>
                  ) : (
                    <>
                      <XCircle className="h-3.5 w-3.5 text-destructive" />
                      <span className="font-semibold text-destructive uppercase tracking-wider text-[10px]">
                        failed
                      </span>
                    </>
                  )}
                  <span className="text-muted-foreground font-mono">
                    {formatRelative(lastRanAt)}
                  </span>
                </div>
                <ul className="space-y-1 font-mono text-[11px]">
                  <li className="flex justify-between gap-3">
                    <span className="text-muted-foreground">Crew</span>
                    <span>{lastResult.crew_slug}</span>
                  </li>
                  {lastResult.canary_path && (
                    <li className="flex justify-between gap-3">
                      <span className="text-muted-foreground">Canary path</span>
                      <span className="truncate text-foreground/80" title={lastResult.canary_path}>
                        {lastResult.canary_path}
                      </span>
                    </li>
                  )}
                  {lastResult.canary_bytes > 0 && (
                    <li className="flex justify-between gap-3">
                      <span className="text-muted-foreground">Canary size</span>
                      <span>{lastResult.canary_bytes} B</span>
                    </li>
                  )}
                  {lastResult.bundle_bytes > 0 && (
                    <li className="flex justify-between gap-3">
                      <span className="text-muted-foreground">Bundle size</span>
                      <span>{formatBytes(lastResult.bundle_bytes)}</span>
                    </li>
                  )}
                  <li className="flex justify-between gap-3">
                    <span className="text-muted-foreground">Elapsed</span>
                    <span>{lastResult.elapsed_ms} ms</span>
                  </li>
                  {lastResult.error && (
                    <li className="flex justify-between gap-3 text-destructive">
                      <span>Error</span>
                      <span className="truncate" title={lastResult.error}>
                        {lastResult.error}
                      </span>
                    </li>
                  )}
                </ul>
              </div>
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  )
}
