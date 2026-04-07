/**
 * Pure layout utilities for the orchestration workflow graph.
 * Extracted for testability — no React or ReactFlow dependencies.
 */

// --- Constants ---

export const LAYOUT = {
  CREW_GAP: 80,
  TASK_WIDTH: 260,
  TASK_HEIGHT: 150,
  TASK_H_GAP: 60,
  TASK_V_GAP: 15,
  CREW_PADDING_TOP: 60,
  CREW_PADDING_SIDE: 40,
  CREW_PADDING_BOTTOM: 40,
  COLLAPSED_WIDTH: 300,
  COLLAPSED_HEIGHT: 50,
} as const

export const statusColors: Record<string, string> = {
  COMPLETED: "#22c55e",
  IN_PROGRESS: "#3b82f6",
  FAILED: "#ef4444",
  BLOCKED: "#f59e0b",
  PENDING: "#64748b",
  PLANNING: "#8b5cf6",
  REVIEW: "#a855f7",
  CANCELLED: "#6b7280",
  SKIPPED: "#6b7280",
  AWAITING_APPROVAL: "#8b5cf6",
}

export const edgeColorPalette = [
  "#06b6d4", "#3b82f6", "#8b5cf6", "#22c55e",
  "#f59e0b", "#ec4899", "#14b8a6", "#6366f1",
]

// --- Pure functions ---

/**
 * Safe parser for depends_on JSON field.
 * Handles null, undefined, non-array, and malformed JSON.
 */
export function parseDependsOn(raw: string | null | undefined): string[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === "string") : []
  } catch {
    return []
  }
}

/**
 * Deterministic edge color based on hash of source+target IDs.
 */
export function pickEdgeColor(sourceId: string, targetId: string): string {
  let h = 0
  const key = sourceId + targetId
  for (let i = 0; i < key.length; i++) h = ((h << 5) - h + key.charCodeAt(i)) | 0
  return edgeColorPalette[Math.abs(h) % edgeColorPalette.length]
}

/**
 * Compute topological levels for tasks based on dependency DAG.
 * Returns a Map of taskId → level (0 = no dependencies).
 * Handles cycles by breaking recursion and returning 0.
 */
export function computeTopologicalLevels(
  taskIds: string[],
  dependencyMap: Map<string, string[]>
): Map<string, number> {
  const levels = new Map<string, number>()
  const visiting = new Set<string>()

  function getLevel(taskId: string): number {
    if (levels.has(taskId)) return levels.get(taskId)!
    if (visiting.has(taskId)) return 0 // cycle detected
    visiting.add(taskId)
    const deps = dependencyMap.get(taskId) || []
    if (deps.length === 0) {
      levels.set(taskId, 0)
      return 0
    }
    const level = Math.max(...deps.map(getLevel)) + 1
    levels.set(taskId, level)
    return level
  }

  for (const id of taskIds) getLevel(id)
  return levels
}

/**
 * Group items by their topological level.
 */
export function groupByLevel<T>(
  items: T[],
  getId: (item: T) => string,
  levels: Map<string, number>
): Map<number, T[]> {
  const groups = new Map<number, T[]>()
  for (const item of items) {
    const level = levels.get(getId(item)) || 0
    if (!groups.has(level)) groups.set(level, [])
    groups.get(level)!.push(item)
  }
  return groups
}

/**
 * Compute crew group dimensions based on task layout.
 */
export function computeCrewDimensions(
  maxLevel: number,
  maxLevelSize: number
): { width: number; height: number } {
  return {
    width: (maxLevel + 1) * (LAYOUT.TASK_WIDTH + LAYOUT.TASK_H_GAP) + LAYOUT.CREW_PADDING_SIDE * 2,
    height: maxLevelSize * (LAYOUT.TASK_HEIGHT + LAYOUT.TASK_V_GAP) + LAYOUT.CREW_PADDING_TOP + LAYOUT.CREW_PADDING_BOTTOM,
  }
}

/**
 * Compute position of a task within a crew group.
 */
export function computeTaskPosition(level: number, index: number): { x: number; y: number } {
  return {
    x: LAYOUT.CREW_PADDING_SIDE + level * (LAYOUT.TASK_WIDTH + LAYOUT.TASK_H_GAP),
    y: LAYOUT.CREW_PADDING_TOP + index * (LAYOUT.TASK_HEIGHT + LAYOUT.TASK_V_GAP),
  }
}

/**
 * Select active missions, or fall back to most recent N.
 */
export function selectActiveMissions<T extends { status: string; updated_at: string }>(
  missions: T[],
  fallbackCount = 3
): T[] {
  const active = missions.filter(
    (m) => m.status === "IN_PROGRESS" || m.status === "PLANNING" || m.status === "REVIEW"
  )
  if (active.length > 0) return active
  return [...missions]
    .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
    .slice(0, fallbackCount)
}
