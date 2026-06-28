"use client"

import { useCallback, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Play, ScrollText } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { SectionHeader, PropertyRow } from "@/components/features/issues/property-row"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import type { Mission } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"

interface IssueRoutineBinderProps {
  issue: Mission
  routines: Pipeline[]
  workspaceId: string
  patchIssue: (patch: Record<string, unknown>) => Promise<void>
  onUpdated: () => void
}

export function IssueRoutineBinder({
  issue,
  routines,
  workspaceId,
  patchIssue,
  onUpdated,
}: IssueRoutineBinderProps) {
  const [routineSectionOpen, setRoutineSectionOpen] = useState(true)
  const [routinePickerOpen, setRoutinePickerOpen] = useState(false)
  const [runningRoutine, setRunningRoutine] = useState(false)

  // Run the bound routine. Resolves the issue's routine_slug, fires
  // the existing /pipelines/{slug}/run endpoint with empty inputs
  // (routine_inputs would be merged in here once the inputs editor
  // ships), and surfaces a toast pointing at /activity for live
  // progress. Disabled when routineId or slug is missing.
  const runBoundRoutine = useCallback(async () => {
    if (!issue.routine_slug) return
    setRunningRoutine(true)
    try {
      // triggered_by_id carries the issue identifier (ENG-15) so the
      // /activity Runs view's LEFT JOIN missions ON triggered_by_id =
      // identifier resolves the source pill back to the issue. We
      // fall back to issue.id only when identifier is absent (shouldn't
      // happen for issues but defends the type).
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${encodeURIComponent(issue.routine_slug)}/run`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            inputs: {},
            triggered_via: "issue",
            triggered_by_id: issue.identifier ?? issue.id,
          }),
        },
      )
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? "Failed to start routine")
        return
      }
      toast.success(`Routine ${issue.routine_slug} started — see /activity`)
      onUpdated()
    } catch {
      toast.error("Failed to start routine")
    } finally {
      setRunningRoutine(false)
    }
  }, [issue.routine_slug, issue.identifier, issue.id, workspaceId, onUpdated])

  if (routines.length === 0) return null

  return (
    <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-background">
      <SectionHeader
        title="Routine"
        open={routineSectionOpen}
        onToggle={() => setRoutineSectionOpen((v) => !v)}
        action={
          issue.routine_slug ? (
            <button
              onClick={runBoundRoutine}
              disabled={runningRoutine}
              className={cn(
                "flex items-center gap-1 rounded px-2 py-0.5 text-[10px] font-medium transition-colors",
                runningRoutine
                  ? "bg-blue-500/10 text-blue-400/60"
                  : "bg-emerald-500/15 text-emerald-300 hover:bg-emerald-500/25",
              )}
            >
              <Play className="h-3 w-3" />
              {runningRoutine ? "Starting…" : "Run"}
            </button>
          ) : undefined
        }
      />
      <AnimatePresence initial={false}>
        {routineSectionOpen && (
          <motion.div
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.2 }}
          >
            <Popover open={routinePickerOpen} onOpenChange={setRoutinePickerOpen}>
              <PopoverTrigger asChild>
                <div>
                  <PropertyRow label="Routine">
                    <ScrollText className="h-3.5 w-3.5 text-muted-foreground" />
                    {issue.routine_name ? (
                      <span className="truncate">{issue.routine_name}</span>
                    ) : (
                      <span className="text-foreground/40">No routine bound</span>
                    )}
                  </PropertyRow>
                </div>
              </PopoverTrigger>
              <PopoverContent className="w-[280px] p-0" align="start" sideOffset={4}>
                <Command>
                  <CommandInput placeholder="Search routines..." className="h-8 text-xs" />
                  <CommandList>
                    <CommandEmpty>No routines yet.</CommandEmpty>
                    <CommandGroup>
                      <CommandItem
                        value="__none__"
                        onSelect={() => {
                          patchIssue({ routine_id: "" })
                          setRoutinePickerOpen(false)
                        }}
                        className="flex items-center gap-2 text-xs"
                      >
                        <span className="text-muted-foreground">No routine</span>
                        {!issue.routine_id && <span className="ml-auto text-blue-400 text-[10px]">current</span>}
                      </CommandItem>
                      {routines.map((r) => (
                        <CommandItem
                          key={r.id}
                          value={`${r.name} ${r.slug}`}
                          onSelect={() => {
                            patchIssue({ routine_id: r.id })
                            setRoutinePickerOpen(false)
                          }}
                          className="flex items-center gap-2 text-xs"
                        >
                          <ScrollText className="h-3 w-3 shrink-0 text-muted-foreground" />
                          <div className="min-w-0 flex-1">
                            <div className="font-medium truncate">{r.name}</div>
                            <div className="text-[10px] text-muted-foreground truncate">{r.slug}</div>
                          </div>
                          {issue.routine_id === r.id && (
                            <span className="ml-2 shrink-0 text-blue-400 text-[10px]">current</span>
                          )}
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}
