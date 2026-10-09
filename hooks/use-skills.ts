"use client"

import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type {
  ProposedSkill,
  SkillDetail,
  SkillRow,
  SkillsAgent,
  SkillsCrew,
} from "@/components/features/skills/skills-model"

// Data for the Skills page (#3033): the catalog with this workspace's
// holders and usage (#3032), the workspace's agents and crews for the
// explorer, staged proposals per crew, and the writes the page makes.

export const skillsKeys = {
  all: (ws: string) => ["skills", ws] as const,
  list: (ws: string) => ["skills", ws, "list"] as const,
  detail: (ws: string, id: string) => ["skills", ws, "detail", id] as const,
  agents: (ws: string) => ["skills", ws, "agents"] as const,
  crews: (ws: string) => ["skills", ws, "crews"] as const,
  proposed: (ws: string, crewId: string) => ["skills", ws, "proposed", crewId] as const,
}

const ws = (id: string) => `workspace_id=${encodeURIComponent(id)}`

/** A failed write, worded for a toast: the server's detail when it sent one. */
export async function skillsErrorMessage(res: Response, fallback: string): Promise<string> {
  try {
    const body = await res.json()
    return body?.detail || body?.error || body?.message || fallback
  } catch {
    return fallback
  }
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await apiFetch(path, { signal })
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return (await res.json()) as T
}

export function useSkillsList(workspaceId: string | null | undefined) {
  const queryClient = useQueryClient()
  // Another tab or the CLI assigning a skill changes holders and counts.
  useRealtimeEventSafe("agent.skill_assigned", () => {
    if (workspaceId) void queryClient.invalidateQueries({ queryKey: skillsKeys.all(workspaceId) })
  })
  useRealtimeEventSafe("agent.skill_unassigned", () => {
    if (workspaceId) void queryClient.invalidateQueries({ queryKey: skillsKeys.all(workspaceId) })
  })
  return useQuery({
    queryKey: skillsKeys.list(workspaceId ?? ""),
    enabled: !!workspaceId,
    queryFn: ({ signal }) => getJSON<SkillRow[]>(`/api/v1/skills?${ws(workspaceId!)}`, signal).then((r) => r ?? []),
  })
}

export function useSkillDetail(workspaceId: string | null | undefined, skillId: string | null) {
  return useQuery({
    queryKey: skillsKeys.detail(workspaceId ?? "", skillId ?? ""),
    enabled: !!workspaceId && !!skillId,
    queryFn: ({ signal }) =>
      getJSON<SkillDetail>(`/api/v1/skills/${encodeURIComponent(skillId!)}?${ws(workspaceId!)}`, signal),
  })
}

export function useSkillsAgents(workspaceId: string | null | undefined) {
  return useQuery({
    queryKey: skillsKeys.agents(workspaceId ?? ""),
    enabled: !!workspaceId,
    queryFn: ({ signal }) => getJSON<SkillsAgent[]>(`/api/v1/agents?${ws(workspaceId!)}`, signal).then((r) => r ?? []),
  })
}

export function useSkillsCrews(workspaceId: string | null | undefined) {
  return useQuery({
    queryKey: skillsKeys.crews(workspaceId ?? ""),
    enabled: !!workspaceId,
    queryFn: ({ signal }) => getJSON<SkillsCrew[]>(`/api/v1/crews?${ws(workspaceId!)}`, signal).then((r) => r ?? []),
  })
}

/**
 * Staged proposals across the workspace's crews. Reviewing them needs a
 * manager role: for anyone else the endpoint refuses and the page simply
 * has nothing to review, so a refusal reads as an empty list.
 */
export function useProposedSkills(workspaceId: string | null | undefined, crewIds: string[]) {
  const results = useQueries({
    queries: crewIds.map((crewId) => ({
      queryKey: skillsKeys.proposed(workspaceId ?? "", crewId),
      enabled: !!workspaceId,
      queryFn: async ({ signal }: { signal?: AbortSignal }) => {
        const res = await apiFetch(`/api/v1/skills/proposed?crew_id=${encodeURIComponent(crewId)}&${ws(workspaceId!)}`, { signal })
        if (!res.ok) return [] as ProposedSkill[]
        const rows = ((await res.json()) ?? []) as Omit<ProposedSkill, "crew_id">[]
        return rows.map((r) => ({ ...r, crew_id: crewId }))
      },
    })),
  })
  return {
    proposed: results.flatMap((r) => r.data ?? []),
    loading: results.some((r) => r.isLoading),
  }
}

/** Give a skill to an agent, or take it away. */
export function useSetSkillAssignment(workspaceId: string | null | undefined) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ agentId, skillId, on }: { agentId: string; skillId: string; on: boolean }) => {
      const base = `/api/v1/agents/${encodeURIComponent(agentId)}/skills`
      const res = on
        ? await apiFetch(`${base}?${ws(workspaceId!)}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ skill_id: skillId }),
          })
        : await apiFetch(`${base}/${encodeURIComponent(skillId)}?${ws(workspaceId!)}`, { method: "DELETE" })
      // Already in the state asked for is not a failure.
      if (!res.ok && res.status !== 409 && !(res.status === 404 && !on)) {
        throw new Error(await skillsErrorMessage(res, on ? "Could not assign the skill" : "Could not remove the skill"))
      }
    },
    onSettled: () => {
      if (workspaceId) void queryClient.invalidateQueries({ queryKey: skillsKeys.all(workspaceId) })
    },
  })
}

/** Approve or reject a staged proposal. */
export function useReviewProposedSkill(workspaceId: string | null | undefined) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ proposal, approve }: { proposal: ProposedSkill; approve: boolean }) => {
      const res = await apiFetch(`/api/v1/skills/proposed/${approve ? "approve" : "reject"}?${ws(workspaceId!)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ crew_id: proposal.crew_id, file_name: proposal.file_name }),
      })
      if (!res.ok) throw new Error(await skillsErrorMessage(res, approve ? "Could not approve the proposal" : "Could not reject the proposal"))
    },
    onSettled: () => {
      if (workspaceId) void queryClient.invalidateQueries({ queryKey: skillsKeys.all(workspaceId) })
    },
  })
}

/** Write a new skill with the workspace's Anthropic key (POST …/skills/generate). */
export function useGenerateSkill(workspaceId: string | null | undefined) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ slug, prompt }: { slug: string; prompt: string }) => {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId!)}/skills/generate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ slug, prompt }),
      })
      if (!res.ok) throw new Error(await skillsErrorMessage(res, "Could not write the skill"))
      return (await res.json()) as { skill_id: string; slug: string; content: string }
    },
    onSuccess: () => {
      if (workspaceId) void queryClient.invalidateQueries({ queryKey: skillsKeys.all(workspaceId) })
    },
  })
}

/**
 * Save a SKILL.md into the catalog (POST …/skills/import with `content`).
 *
 * The importer upserts by the frontmatter `name`, so the same call creates a
 * skill, edits one the workspace imported or generated, and sets the icon and
 * domain the generator does not. It refuses to overwrite a built-in skill and
 * re-runs the import scan on every save.
 */
export function useImportSkill(workspaceId: string | null | undefined) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ content }: { content: string }) => {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId!)}/skills/import`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content }),
      })
      if (!res.ok) throw new Error(await skillsErrorMessage(res, "Could not save the skill"))
      return (await res.json()) as { skill_id: string; slug: string; name: string; created: boolean }
    },
    onSuccess: () => {
      if (workspaceId) void queryClient.invalidateQueries({ queryKey: skillsKeys.all(workspaceId) })
    },
  })
}

/** Remove a skill from the workspace catalog (not possible for built-ins). */
export function useDeleteSkill(workspaceId: string | null | undefined) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (skillId: string) => {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId!)}/skills/${encodeURIComponent(skillId)}`, {
        method: "DELETE",
      })
      if (!res.ok) throw new Error(await skillsErrorMessage(res, "Could not delete the skill"))
    },
    onSettled: () => {
      if (workspaceId) void queryClient.invalidateQueries({ queryKey: skillsKeys.all(workspaceId) })
    },
  })
}
