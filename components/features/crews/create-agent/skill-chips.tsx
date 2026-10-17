"use client"

import { useEffect, useMemo, useState } from "react"
import { Search } from "lucide-react"

import { CreateSurfaceField } from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"

export interface WorkspaceSkillLite { id: string; name: string; display_name?: string | null; description?: string | null }

/** Assign each picked skill to a new agent; returns the names that failed. */
export async function assignSkills(workspaceId: string, agentId: string, skills: WorkspaceSkillLite[]): Promise<string[]> {
  const failed: string[] = []
  for (const s of skills) {
    try {
      const r = await apiFetch(`/api/v1/agents/${agentId}/skills?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ skill_id: s.id }),
      })
      if (!r.ok) failed.push(`skill ${s.display_name || s.name}`)
    } catch {
      failed.push(`skill ${s.display_name || s.name}`)
    }
  }
  return failed
}

/**
 * New agent › Access › Skills: the workspace's skills as chips; the picked
 * ones are assigned once the agent exists (POST /agents/{id}/skills).
 */
export function SkillChips({ workspaceId, value, onChange, enabled }: {
  workspaceId: string
  value: WorkspaceSkillLite[]
  onChange: (next: WorkspaceSkillLite[]) => void
  /** Read the catalogue only once the step is on screen. */
  enabled: boolean
}) {
  const [skills, setSkills] = useState<WorkspaceSkillLite[] | null>(null)
  const [failed, setFailed] = useState(false)
  const [query, setQuery] = useState("")

  useEffect(() => {
    if (!enabled || skills !== null) return
    let cancelled = false
    apiFetch(`/api/v1/skills?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((body: unknown) => { if (!cancelled) setSkills(Array.isArray(body) ? (body as WorkspaceSkillLite[]) : []) })
      .catch(() => { if (!cancelled) { setSkills([]); setFailed(true) } })
    return () => { cancelled = true }
  }, [enabled, skills, workspaceId])

  const picked = useMemo(() => new Set(value.map((s) => s.id)), [value])
  const needle = query.trim().toLocaleLowerCase()
  const shown = (skills ?? []).filter((s) => !needle || `${s.display_name ?? ""} ${s.name}`.toLocaleLowerCase().includes(needle))

  return (
    <CreateSurfaceField label="Skills" hint={value.length ? `${value.length} picked · assigned right after the agent is created` : "Optional · instructions only; a skill never runs anything by itself"}>
      {failed ? (
        <p role="alert" className="text-label text-chip-danger-fg">Skills could not be loaded. Add them later under Tools and access.</p>
      ) : skills === null ? (
        <p className="text-label text-muted-foreground">Loading skills…</p>
      ) : skills.length === 0 ? (
        <p className="text-label text-muted-foreground">No skills in this workspace yet.</p>
      ) : (
        <div className="space-y-2">
          {skills.length > 8 && (
            <label className="flex h-8 items-center gap-2 rounded-md border border-hairline bg-background px-2.5 coarse:h-[44px]">
              <Search className="size-3.5 text-muted-foreground" aria-hidden />
              <input aria-label="Find a skill" placeholder="Find a skill" value={query} onChange={(e) => setQuery(e.target.value)} className="min-w-0 flex-1 bg-transparent text-xs outline-none" />
            </label>
          )}
          <div className="flex flex-wrap gap-1.5" role="group" aria-label="Skills">
            {shown.map((s) => {
              const on = picked.has(s.id)
              return (
                <button
                  key={s.id}
                  type="button"
                  aria-pressed={on}
                  title={s.description ?? undefined}
                  onClick={() => onChange(on ? value.filter((v) => v.id !== s.id) : [...value, s])}
                  className={cn(
                    "inline-flex h-7 items-center rounded-md border px-2.5 text-label transition-colors coarse:min-h-[44px]",
                    on ? "border-primary bg-primary/10 text-foreground" : "border-border text-muted-foreground hover:text-foreground",
                  )}
                >
                  {s.display_name || s.name}
                </button>
              )
            })}
          </div>
        </div>
      )}
    </CreateSurfaceField>
  )
}
