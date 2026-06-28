"use client"

import {
  useQuery,
  useMutation,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

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

/**
 * BackupScopeLevel selects which per-crew filesystem sections the
 * backup includes:
 *
 *  - quick:    workspace + agent memory (~1 MiB, snapshot of active work)
 *  - standard: + /home/agent + /opt/crew-tools (default; matches current behaviour)
 *  - full:     + /var/lib (any in-container service data — redis, postgresql, ...)
 *
 * Older bundles produced before the preset feature read back as
 * "standard" via the catalog migration's column default.
 */
export type BackupScopeLevel = "quick" | "standard" | "full"

export interface BackupListEntry {
  path: string
  file_name: string
  size_bytes: number
  scope: BackupScope
  scope_level?: BackupScopeLevel
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
  scope_level?: BackupScopeLevel
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
  scope_level?: BackupScopeLevel
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
  scope_level?: BackupScopeLevel
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
      const res = await apiFetch(withQuery("/api/v1/admin/backups", workspaceId!))
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
      const res = await apiFetch(withQuery("/api/v1/admin/backups/status", workspaceId!))
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
      const res = await apiFetch(
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
      const res = await apiFetch(withQuery("/api/v1/admin/backups", ws), {
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
      const res = await apiFetch(withQuery("/api/v1/admin/backups/restore", ws), {
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
      const res = await apiFetch(withQuery("/api/v1/admin/backups", ws, { path }), {
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

// Wire-format types kept aligned with the Go server (see
// internal/api/backup_query.go + backup_admin.go + backup/metrics.go +
// backup/selftest.go). Earlier drafts of these types were guessed; a
// QA pass against the live dev3 API found every one mis-shaped, so
// these now mirror the actual JSON wire.

export interface VerifyBackupResponse {
  // Whether the bundle's SHA matches what the manifest recorded.
  valid: boolean
  size_bytes: number
  // Parsed manifest (same shape as useInspectBackup), echoed back so
  // a caller can show details without a second round-trip.
  manifest: BackupManifest
  // Empty string on success; populated on parse / decrypt / hash
  // failure with the reason text.
  error: string
}

export interface RotateBackupRequest {
  // 0 disables that rule. At least one must be > 0; backend rejects
  // both-zero with a 400.
  keep_last?: number
  keep_days?: number
  dry_run?: boolean
}

export interface RotateBackupResponse {
  // Backend returns null (not [] !) when nothing was eligible. The
  // hook + components treat null and [] as equivalent.
  deleted: string[] | null
  dry_run: boolean
}

export interface SelfTestRequest {
  // Self-test is per-crew (not workspace-wide): the backend writes a
  // canary file inside the named crew's container, snapshots it,
  // mutates it, then restores from the snapshot to verify the loop.
  // Caller must pick a provisioned crew; un-provisioned crews return
  // ok=false with error="container not found".
  crew_id: string
}

export interface SelfTestResponse {
  ok: boolean
  crew_id: string
  crew_slug: string
  // Path of the canary file inside the container (e.g. /workspace/CANARY-<hex>.txt).
  canary_path: string
  canary_bytes: number
  bundle_bytes: number
  elapsed_ms: number
  // Empty string when ok=true; populated with reason on failure
  // (e.g. "container not found (is the crew provisioned?)").
  error?: string
}

export interface BackupMetricsResponse {
  created_total: number
  created_by_scope: Record<string, number>
  failed_total: number
  failed_by_reason: Record<string, number>
  restored_total: number
  size_bytes_total: number
  duration_seconds_p50: number
  duration_seconds_p95: number
  duration_seconds_mean: number
  // Map of workspace_id → seconds the lock has been held (for
  // dashboards that monitor stuck locks across many workspaces).
  lock_held_seconds_by_workspace: Record<string, number>
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
      const res = await apiFetch(
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
      const res = await apiFetch(withQuery("/api/v1/admin/backups/rotate", ws), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(req),
      })
      return asJSON<RotateBackupResponse>(res)
    },
    onSuccess: (result) => {
      // Only invalidate the list when we actually deleted something —
      // dry runs leave disk state untouched, so a refetch would be
      // wasted. Backend returns null (not []) when nothing eligible;
      // the ?? [] normalises both shapes.
      if (!result.dry_run && (result.deleted ?? []).length > 0) {
        qc.invalidateQueries({ queryKey: ["backups", workspaceId] })
      }
    },
  })
}

/**
 * Canary backup → mutate → restore → verify round-trip against a SINGLE
 * provisioned crew. The backend writes a sentinel file inside the
 * crew's container, snapshots it through the full backup pipeline,
 * overwrites the file, and restores from the snapshot to assert the
 * pipeline produced bit-identical content. Quick (~50ms on a small
 * crew) and safe — the canary file is the only filesystem mutation.
 *
 * The mutation argument is a SelfTestRequest carrying the crew_id;
 * earlier drafts of this hook took no body and the endpoint 400'd.
 * Surface the picker requirement to the UI: a self-test card has to
 * choose which crew to exercise (un-provisioned crews return ok=false
 * with error="container not found").
 */
export function useBackupSelfTest(workspaceId: string | undefined) {
  return useMutation<SelfTestResponse, Error, SelfTestRequest>({
    mutationFn: async (req) => {
      const ws = requireWorkspaceId(workspaceId)
      const res = await apiFetch(withQuery("/api/v1/admin/backups/self-test", ws), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(req),
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
      const res = await apiFetch(withQuery("/api/v1/admin/backups/status", ws), {
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
 * Process-wide backup counters (created/failed/restored totals,
 * size_bytes_total, p50/p95/mean durations). Polled every 30s while
 * the backups tab is mounted.
 *
 * Backend gates this endpoint on `IsInstanceOwner` (env-var
 * CREWSHIP_INSTANCE_OWNER_EMAIL match). Workspace owners who are NOT
 * the instance owner get a 403; the hook lets that surface as a
 * normal error so the UI can render a small "metrics require instance
 * owner" hint instead of a blank silent failure. (Metrics are
 * process-global, so showing them to non-instance-owners would leak
 * other workspaces' counters in a multi-tenant deployment — the
 * gate is intentional.)
 */
export function useBackupMetrics(workspaceId: string | undefined) {
  return useQuery<BackupMetricsResponse>({
    queryKey: ["backup-metrics", workspaceId],
    queryFn: async () => {
      const res = await apiFetch(withQuery("/api/v1/admin/backups/metrics", workspaceId!))
      return asJSON<BackupMetricsResponse>(res)
    },
    enabled: Boolean(workspaceId),
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    // Do NOT retry the 403 — instance-owner gating is a permission,
    // not a transient failure. Without this, the row spams the
    // endpoint every retry interval.
    retry: false,
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
      const res = await apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId!)}`)
      const body = await asJSON<{ data?: CrewLite[] } | CrewLite[]>(res)
      // The /crews endpoint historically returned a bare array; the
      // newer paginated handler wraps it under { data }. Accept both.
      return Array.isArray(body) ? body : (body.data ?? [])
    },
    enabled: Boolean(workspaceId),
    staleTime: 60_000,
  })
}
