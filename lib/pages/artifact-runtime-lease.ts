import { apiFetch } from '@/lib/api-fetch'
import type { ArtifactRuntimeIdentity } from './artifact-runtime-channel'

/** Immutable source metadata, not a runtime capability or executable profile. */
export interface ArtifactLeaseReference {
  pageId: string
  slug: string
  revision: number
  contentSHA256: string
  runtimeProfile: 'static-html-v1'
}
export interface DisabledArtifactLeaseMetadata {
  pageId: string
  revision: number
  contentSHA256: string
  runtimeProfile: 'static-html-v1'
  expiresAt: number
  execution: { enabled: false; reason: 'feature_disabled' }
}
export class ArtifactRuntimeLeaseError extends Error {}
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === 'object' && !Array.isArray(value)
const digest = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value)

export function parseDisabledArtifactLease(value: unknown, expected: ArtifactLeaseReference, now = Date.now()): DisabledArtifactLeaseMetadata {
  if (!expected.pageId || expected.pageId.length > 128 || !expected.slug || expected.slug.length > 256 || !Number.isSafeInteger(expected.revision) || expected.revision < 1 || !digest(expected.contentSHA256) || expected.runtimeProfile !== 'static-html-v1' || !Number.isFinite(now)) throw new ArtifactRuntimeLeaseError('Invalid exact revision reference.')
  if (!object(value) || Object.keys(value).sort().join(',') !== 'content_sha256,execution,expires_at,page_id,revision,runtime_profile'
    || value.page_id !== expected.pageId || value.revision !== expected.revision || value.content_sha256 !== expected.contentSHA256 || value.runtime_profile !== expected.runtimeProfile) throw new ArtifactRuntimeLeaseError('Artifact lease does not match the exact source revision.')
  // There is deliberately no speculative enabled:true branch. A future
  // admission protocol requires its own review; static metadata is not consent.
  if (!object(value.execution) || Object.keys(value.execution).sort().join(',') !== 'enabled,reason' || value.execution.enabled !== false || value.execution.reason !== 'feature_disabled') throw new ArtifactRuntimeLeaseError('Unsupported artifact execution descriptor.')
  if (typeof value.expires_at !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value.expires_at)) throw new ArtifactRuntimeLeaseError('Invalid artifact lease expiry.')
  const expiresAt = Date.parse(value.expires_at)
  if (!Number.isFinite(expiresAt) || new Date(expiresAt).toISOString().slice(0, 19) !== value.expires_at.slice(0, 19) || expiresAt <= now || expiresAt > now + 15_000) throw new ArtifactRuntimeLeaseError('Artifact lease expired or exceeds the supported lifetime.')
  return { pageId: expected.pageId, revision: expected.revision, contentSHA256: expected.contentSHA256, runtimeProfile: expected.runtimeProfile, expiresAt, execution: { enabled: false, reason: 'feature_disabled' } }
}

/** Preparation adapter only. It checks host metadata without refetching HTML,
 * and always refuses execution under the current hard-OFF protocol. */
export function createDisabledArtifactRuntimeLease(workspaceId: string, expected: ArtifactLeaseReference) {
  // Snapshot caller inputs; later selection changes must create another adapter.
  const reference = { ...expected }
  return async (identity: ArtifactRuntimeIdentity, signal: AbortSignal): Promise<{ expiresAt: number }> => {
    if (signal.aborted) throw new DOMException('Lease check cancelled.', 'AbortError')
    if (!workspaceId || identity.artifactId !== reference.pageId || identity.revision !== reference.revision || identity.profile !== 'interactive-html-v1') throw new ArtifactRuntimeLeaseError('Runtime identity differs from the selected source revision.')
    const params = new URLSearchParams({ workspace_id: workspaceId, revision: String(reference.revision) })
    const boundedSignal = AbortSignal.any([signal, AbortSignal.timeout(5000)])
    const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(reference.slug)}/artifact/lease?${params}`, { signal: boundedSignal, cache: 'no-store' })
    if (signal.aborted) throw new DOMException('Lease check cancelled.', 'AbortError')
    if (!response.ok) throw new ArtifactRuntimeLeaseError('Current artifact access could not be verified.')
    // Metadata is bounded independently of the 10MiB source endpoint.
    const reader = response.body?.getReader()
    if (!reader) throw new ArtifactRuntimeLeaseError('Artifact lease metadata is missing.')
    const cancelRead = () => { void reader.cancel().catch(() => {}) }
    boundedSignal.addEventListener('abort', cancelRead, { once: true })
    const decoder = new TextDecoder('utf-8', { fatal: true })
    let text = ''; let bytes = 0
    try {
      while (true) {
        const chunk = await reader.read()
        boundedSignal.throwIfAborted()
        if (chunk.done) break
        bytes += chunk.value.byteLength
        if (bytes > 4096) throw new ArtifactRuntimeLeaseError('Artifact lease metadata exceeds its limit.')
        text += decoder.decode(chunk.value, { stream: true })
      }
      text += decoder.decode()
    } finally { boundedSignal.removeEventListener('abort', cancelRead); await reader.cancel().catch(() => {}); reader.releaseLock() }
    parseDisabledArtifactLease(JSON.parse(text), reference)
    throw new ArtifactRuntimeLeaseError('Interactive artifact execution is disabled.')
  }
}
