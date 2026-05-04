"use client"

import {
  useQuery,
  useMutation,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query"

/**
 * React Query surface for the admin backups panel (CRE-128). One
 * cache per endpoint; mutations invalidate the shared `backups` key
 * so the list refreshes after create / delete without a manual
 * `refetch()` from every caller.
 *
 * All fetch calls rely on the session cookie set by NextAuth. RFC
 * 7807 / plain-text errors flow through `asError` so `onError`
 * handlers render human-friendly messages via sonner.
 */

/**
 * Bundles coming BACK from the server can be any scope (the manifest
 * on disk may have been produced by a newer server that supports
 * instance scope). Creating a new backup is narrower — the current
 * REST contract only accepts "crew" or "workspace"; instance scope is
 * wired through the CLI path only. Keeping the types distinct prevents
 * the UI from compiling a request the server will always reject.
 */
export type BackupScope = "crew" | "workspace" | "instance"
export type CreateBackupScope = Exclude<BackupScope, "instance">

export interface BackupListEntry {
  path: string
  file_name: string
  size_bytes: number
  scope: BackupScope
  encrypted: boolean
  created_at?: string
  format_version?: number
}

export interface BackupStatus {
  workspace_id: string
  /** Server field is `held` (matches lock semantics in the DB). An
   * earlier draft used `locked` here — renamed so consumers match
   * what `/api/v1/admin/backups/status` actually returns. */
  held: boolean
  acquired_by?: string
  acquired_at?: string
  expires_at?: string
}

export interface BackupManifest {
  format_version: number
  crewship_version_at_backup: string
  scope: BackupScope
  created_at: string
  checksums: { payload_sha256: string }
  encryption: {
    enabled: boolean
    algorithm?: string
    key_derivation?: string
    recipients?: string[]
  }
  contents: {
    workspace?: { id: string; slug: string; name: string }
    crews: Array<{
      id: string
      slug: string
      name: string
      agent_count?: number
    }>
  }
}

export interface CreateBackupRequest {
  scope: CreateBackupScope
  crew_id?: string
  passphrase?: string
  recipient?: string
  no_encrypt?: boolean
  output_dir?: string
}

export interface CreateBackupResponse {
  path: string
  size_bytes: number
  payload_sha256: string
  format_version: number
  scope: BackupScope
  created_at: string
  encrypted: boolean
}

export interface RestoreBackupRequest {
  path: string
  passphrase?: string
  as_workspace?: string
  as_crew?: string
  dry_run?: boolean
}

async function asError(res: Response): Promise<Error> {
  const text = await res.text()
  try {
    const json = JSON.parse(text) as { error?: string; detail?: string }
    return new Error(json.error || json.detail || `HTTP ${res.status}`)
  } catch {
    return new Error(text || `HTTP ${res.status}`)
  }
}

async function asJSON<T>(res: Response): Promise<T> {
  if (!res.ok) {
    throw await asError(res)
  }
  return (await res.json()) as T
}

/**
 * Fail fast when the caller has not yet resolved a workspace. The
 * alternative is a fetch call with `workspace_id=undefined`, which the
 * server silently maps to "no workspace" and then returns a 400 that
 * the UI renders as a generic error — debugging that chain is painful
 * for someone who has never touched the admin panel before.
 */
function requireWorkspaceId(workspaceId: string | undefined): string {
  if (!workspaceId) {
    throw new Error("workspaceId is required for this mutation")
  }
  return workspaceId
}

/**
 * Build a URL with properly encoded query params. Raw string
 * interpolation breaks when a workspace slug or bundle path contains
 * `&`, `=`, or spaces — URLSearchParams handles percent-encoding
 * uniformly so the server always sees the intended values.
 */
function withQuery(
  path: string,
  workspaceId: string,
  extra?: Record<string, string>,
): string {
  const params = new URLSearchParams({ workspace_id: workspaceId, ...(extra ?? {}) })
  return `${path}?${params.toString()}`
}

export function useBackups(
  workspaceId: string | undefined,
  options?: Omit<UseQueryOptions<BackupListEntry[]>, "queryKey" | "queryFn">,
) {
  // Destructure `enabled` so it does NOT bleed in via ...rest and
  // override our workspace guard. A caller who tries to force
  // enabled:true without a workspace would otherwise trigger a fetch
  // with workspace_id=undefined — the guard is load-bearing.
  const { enabled: _ignored, ...rest } = options ?? {}
  void _ignored
  return useQuery<BackupListEntry[]>({
    queryKey: ["backups", workspaceId],
    queryFn: async () => {
      const res = await fetch(withQuery("/api/v1/admin/backups", workspaceId!))
      const body = await asJSON<{ data: BackupListEntry[] }>(res)
      return body.data ?? []
    },
    ...rest,
    enabled: Boolean(workspaceId),
  })
}

export function useBackupStatus(workspaceId: string | undefined) {
  return useQuery<BackupStatus>({
    queryKey: ["backup-status", workspaceId],
    queryFn: async () => {
      const res = await fetch(withQuery("/api/v1/admin/backups/status", workspaceId!))
      return asJSON<BackupStatus>(res)
    },
    enabled: Boolean(workspaceId),
    // Poll while the lock banner is mounted so the admin sees the
    // lock release as soon as a backup finishes. Disable in the
    // background so a minimised tab does not burn bandwidth on a
    // status the user cannot see anyway.
    refetchInterval: 5_000,
    refetchIntervalInBackground: false,
  })
}

export function useInspectBackup(workspaceId: string | undefined, path: string | null) {
  return useQuery<BackupManifest>({
    queryKey: ["backup-inspect", workspaceId, path],
    queryFn: async () => {
      const res = await fetch(
        withQuery("/api/v1/admin/backups/inspect", workspaceId!, { path: path! }),
      )
      return asJSON<BackupManifest>(res)
    },
    enabled: Boolean(workspaceId && path),
  })
}

export function useCreateBackup(workspaceId: string | undefined) {
  const qc = useQueryClient()
  return useMutation<CreateBackupResponse, Error, CreateBackupRequest>({
    mutationFn: async (req) => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await fetch(withQuery("/api/v1/admin/backups", ws), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(req),
      })
      return asJSON<CreateBackupResponse>(res)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backups", workspaceId] })
      qc.invalidateQueries({ queryKey: ["backup-status", workspaceId] })
    },
  })
}

export function useRestoreBackup(workspaceId: string | undefined) {
  const qc = useQueryClient()
  return useMutation<unknown, Error, RestoreBackupRequest>({
    mutationFn: async (req) => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await fetch(withQuery("/api/v1/admin/backups/restore", ws), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(req),
      })
      return asJSON<unknown>(res)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backups", workspaceId] })
      // Restore acquires + releases the workspace lock; refresh the
      // status query so the banner reflects reality without waiting
      // for the next 5-second poll.
      qc.invalidateQueries({ queryKey: ["backup-status", workspaceId] })
    },
  })
}

export function useDeleteBackup(workspaceId: string | undefined) {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (path) => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await fetch(withQuery("/api/v1/admin/backups", ws, { path }), {
        method: "DELETE",
      })
      if (!res.ok) {
        throw await asError(res)
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backups", workspaceId] })
    },
  })
}

// ─── Hooks for previously-unsurfaced backup endpoints ───────────────
//
// The Go backend has shipped these for a while but the UI never called
// them. Surfacing here lets the admin panel offer retention rotation,
// per-bundle integrity verify, direct download, end-to-end self-test,
// recovery from a stuck lock, and live success/fail metrics.

export interface VerifyBackupResponse {
  path: string
  ok: boolean
  payload_sha256: string
  recomputed_sha256: string
  size_bytes: number
}

export interface RotateBackupRequest {
  scope?: BackupScope
  keep_last?: number
  keep_days?: number
  dry_run?: boolean
}

export interface RotateBackupResponse {
  scanned: number
  deleted: Array<{ path: string; size_bytes: number; age_days: number }>
  bytes_reclaimed: number
  dry_run: boolean
}

export interface SelfTestResponse {
  ok: boolean
  duration_ms: number
  trace_id?: string
  steps: Array<{ name: string; ok: boolean; duration_ms: number; error?: string }>
}

export interface BackupMetricsResponse {
  total_bundles: number
  total_size_bytes: number
  encrypted_count: number
  oldest_at?: string
  newest_at?: string
  successes_24h: number
  failures_24h: number
}

export interface CrewLite {
  id: string
  slug: string
  name: string
}

/**
 * Verify recomputes the bundle's payload checksum and compares it to the
 * value stored in the manifest. Cheap (one hash pass) and does not
 * decrypt — safe to call without the passphrase. Surfaces tampering or
 * disk-rot, NOT bad-passphrase / corrupted-after-decrypt.
 */
export function useVerifyBackup(workspaceId: string | undefined) {
  return useMutation<VerifyBackupResponse, Error, string>({
    mutationFn: async (path) => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await fetch(
        withQuery("/api/v1/admin/backups/verify", ws, { path }),
      )
      return asJSON<VerifyBackupResponse>(res)
    },
  })
}

/**
 * Build the streaming-download URL for a bundle. The browser handles
 * the actual download via an `<a href>` so the response can be a large
 * file without it ever passing through React Query's cache. Returns a
 * URL string the caller drops onto an anchor tag.
 */
export function buildDownloadUrl(workspaceId: string, path: string): string {
  return withQuery("/api/v1/admin/backups/download", workspaceId, { path })
}

/**
 * Apply the retention policy: deletes bundles older than `keep_days`
 * UNLESS doing so would drop below `keep_last` total. dry_run=true
 * returns what WOULD be deleted without touching disk — always offer
 * this in the UI before the destructive call.
 */
export function useRotateBackups(workspaceId: string | undefined) {
  const qc = useQueryClient()
  return useMutation<RotateBackupResponse, Error, RotateBackupRequest>({
    mutationFn: async (req) => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await fetch(withQuery("/api/v1/admin/backups/rotate", ws), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(req),
      })
      return asJSON<RotateBackupResponse>(res)
    },
    onSuccess: (result) => {
      // Only invalidate the list when we actually deleted something —
      // dry runs leave disk state untouched, so a refetch would be wasted.
      if (!result.dry_run && result.deleted.length > 0) {
        qc.invalidateQueries({ queryKey: ["backups", workspaceId] })
      }
    },
  })
}

/**
 * Canary backup → destroy → restore → verify round-trip against an
 * isolated test workspace. Validates the backup pipeline is wired
 * correctly without touching real data. Run on demand (button) or on a
 * future schedule (quarterly recommendation per Supabase).
 */
export function useBackupSelfTest(workspaceId: string | undefined) {
  return useMutation<SelfTestResponse, Error, void>({
    mutationFn: async () => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await fetch(withQuery("/api/v1/admin/backups/self-test", ws), {
        method: "POST",
      })
      return asJSON<SelfTestResponse>(res)
    },
  })
}

/**
 * Force-release the per-workspace backup lock. Emergency-only — used
 * when a backup process crashed and left the lock held past its TTL,
 * blocking new backups. Caller must confirm intent (the dialog asks
 * the operator to type "force-unlock") to prevent accidental release
 * during an actually-running backup.
 */
export function useForceUnlock(workspaceId: string | undefined) {
  const qc = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await fetch(withQuery("/api/v1/admin/backups/status", ws), {
        method: "DELETE",
      })
      if (!res.ok) {
        throw await asError(res)
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backup-status", workspaceId] })
    },
  })
}

/**
 * Process-wide backup counters (success/fail in last 24h, total bundles
 * on disk, encrypted percentage, oldest/newest). Cheap call, polled
 * every 30s while the backups tab is mounted to keep the metrics row
 * live without spamming the endpoint.
 */
export function useBackupMetrics(workspaceId: string | undefined) {
  return useQuery<BackupMetricsResponse>({
    queryKey: ["backup-metrics", workspaceId],
    queryFn: async () => {
      const res = await fetch(withQuery("/api/v1/admin/backups/metrics", workspaceId!))
      return asJSON<BackupMetricsResponse>(res)
    },
    enabled: Boolean(workspaceId),
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  })
}

/**
 * Lightweight crew list used to populate the BackupCreateDialog's
 * "crew" picker. Caches separately from the full /crews page query so
 * navigating to that page does not invalidate the picker's list.
 * Consumers receive the minimal shape they need (id/slug/name).
 */
export function useCrewsForBackup(workspaceId: string | undefined) {
  return useQuery<CrewLite[]>({
    queryKey: ["crews-lite", workspaceId],
    queryFn: async () => {
      const res = await fetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId!)}`)
      const body = await asJSON<{ data?: CrewLite[] } | CrewLite[]>(res)
      // The /crews endpoint historically returned a bare array; the
      // newer paginated handler wraps it under { data }. Accept both.
      return Array.isArray(body) ? body : (body.data ?? [])
    },
    enabled: Boolean(workspaceId),
    staleTime: 60_000,
  })
}
