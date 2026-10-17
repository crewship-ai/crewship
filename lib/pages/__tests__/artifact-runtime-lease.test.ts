import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiFetch } from '@/lib/api-fetch'
import { ARTIFACT_RUNTIME_ENABLED } from '../artifact-runtime-channel'
import { createDisabledArtifactRuntimeLease, parseDisabledArtifactLease, type ArtifactLeaseReference } from '../artifact-runtime-lease'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
const now = Date.UTC(2026, 9, 13, 12)
const reference: ArtifactLeaseReference = { pageId: 'page-a', slug: 'private report', revision: 7, contentSHA256: 'a'.repeat(64), runtimeProfile: 'static-html-v1' }
const metadata = () => ({ page_id: reference.pageId, revision: 7, content_sha256: reference.contentSHA256, runtime_profile: 'static-html-v1', expires_at: new Date(now + 15_000).toISOString(), execution: { enabled: false, reason: 'feature_disabled' } })
const identity = { artifactId: reference.pageId, revision: 7, profile: 'interactive-html-v1' as const }
afterEach(() => { vi.useRealTimers(); vi.clearAllMocks() })
describe('exact source lease preparation without execution admission', () => {
  it('parses only exact, short-lived disabled metadata without granting runtime authority', () => {
    expect(ARTIFACT_RUNTIME_ENABLED).toBe(false)
    expect(parseDisabledArtifactLease(metadata(), reference, now)).toEqual({ pageId: reference.pageId, revision: 7, contentSHA256: reference.contentSHA256, runtimeProfile: reference.runtimeProfile, expiresAt: now + 15000, execution: { enabled: false, reason: 'feature_disabled' } })
  })
  it.each([
    { page_id: 'other-page' }, { revision: 8 }, { revision: '7' }, { content_sha256: 'b'.repeat(64) }, { runtime_profile: 'interactive-html-v1' },
    { execution: { enabled: true } }, { execution: { enabled: false, reason: 'other_reason' } }, { execution: null }, { extra_capability: 'forged' },
    { expires_at: new Date(now).toISOString() }, { expires_at: new Date(now + 15001).toISOString() }, { expires_at: 'invalid' },
  ])('fails closed on mismatched, malformed or speculative metadata %j', patch => {
    expect(() => parseDisabledArtifactLease({ ...metadata(), ...patch }, reference, now)).toThrow()
  })
  it('rejects invalid calendar dates, unbounded references and missing fields', () => {
    expect(() => parseDisabledArtifactLease({ ...metadata(), expires_at: '2026-02-30T12:00:00Z' }, reference, Date.UTC(2026, 2, 2, 11, 59, 50))).toThrow()
    expect(() => parseDisabledArtifactLease(metadata(), { ...reference, contentSHA256: 'bad' }, now)).toThrow()
    const { execution: _execution, ...missing } = metadata()
    expect(() => parseDisabledArtifactLease(missing, reference, now)).toThrow()
  })
  it('reads only exact host metadata and still refuses the disabled execution descriptor', async () => {
    vi.useFakeTimers(); vi.setSystemTime(now)
    vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify(metadata())))
    const lease = createDisabledArtifactRuntimeLease('workspace-a', reference)
    await expect(lease(identity, new AbortController().signal)).rejects.toThrow('execution is disabled')
    const [url, options] = vi.mocked(apiFetch).mock.calls[0]
    expect(String(url)).toBe('/api/v1/pages/private%20report/artifact/lease?workspace_id=workspace-a&revision=7')
    expect(options?.cache).toBe('no-store')
    expect(options?.signal).toBeInstanceOf(AbortSignal)
    expect(vi.mocked(apiFetch)).toHaveBeenCalledTimes(1)
  })
  it('does not request another identity, or continue an aborted check', async () => {
    const lease = createDisabledArtifactRuntimeLease('workspace-a', reference)
    await expect(lease({ ...identity, revision: 8 }, new AbortController().signal)).rejects.toThrow('identity differs')
    const abort = new AbortController(); abort.abort()
    await expect(lease(identity, abort.signal)).rejects.toThrow()
    expect(apiFetch).not.toHaveBeenCalled()
  })
  it('refuses oversized metadata and access denial without fetching HTML', async () => {
    const lease = createDisabledArtifactRuntimeLease('workspace-a', reference)
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response('x'.repeat(4097))).mockResolvedValueOnce(new Response('{}', { status: 403 }))
    await expect(lease(identity, new AbortController().signal)).rejects.toThrow('exceeds its limit')
    await expect(lease(identity, new AbortController().signal)).rejects.toThrow('access could not be verified')
    expect(vi.mocked(apiFetch).mock.calls.every(([url]) => String(url).includes('/artifact/lease?'))).toBe(true)
  })
  it('rejects a response arriving after caller cancellation', async () => {
    const abort = new AbortController()
    vi.mocked(apiFetch).mockImplementation(async () => { abort.abort(); return new Response(JSON.stringify(metadata())) })
    await expect(createDisabledArtifactRuntimeLease('workspace-a', reference)(identity, abort.signal)).rejects.toThrow()
  })
  it('honors cancellation after metadata bytes arrive but before body completion', async () => {
    const abort = new AbortController()
    let pulls = 0
    const stream = new ReadableStream<Uint8Array>({
      pull(controller) {
        pulls++
        if (pulls === 1) controller.enqueue(new TextEncoder().encode(JSON.stringify(metadata())))
        else { abort.abort(); controller.close() }
      },
    }, { highWaterMark: 0 })
    vi.mocked(apiFetch).mockResolvedValue(new Response(stream))
    await expect(createDisabledArtifactRuntimeLease('workspace-a', reference)(identity, abort.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(pulls).toBe(2)
  })

})
