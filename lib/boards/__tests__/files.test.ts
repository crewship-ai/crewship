import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiFetch } from '@/lib/api-fetch'
import { loadBoardImages, uploadBoardImage, MAX_BOARD_IMAGE_BYTES } from '../files'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
afterEach(() => { vi.unstubAllGlobals(); vi.resetAllMocks() })
function response(bytes: number, mime = 'image/png', declared?: number) {
  let sent = false
  return { ok: true, headers: new Headers({ 'Content-Type': mime, ...(declared ? { 'Content-Length': String(declared) } : {}) }), body: { getReader: () => ({ read: async () => sent ? { done: true } : (sent = true, { done: false, value: new Uint8Array(bytes) }), cancel: async () => undefined, releaseLock: () => undefined }) } } as unknown as Response
}
function browserImageAPIs() {
  vi.stubGlobal('createImageBitmap', vi.fn(async () => ({ width: 4, height: 3, close: vi.fn() })))
  vi.stubGlobal('FileReader', class { result = 'data:image/png;base64,canonical'; onload?: () => void; readAsDataURL() { queueMicrotask(() => this.onload?.()) } abort() {} })
}
describe('authorized board image bytes', () => {
  it('uses only board-scoped authenticated paths and transient BinaryFiles', async () => {
    browserImageAPIs(); fetchMock.mockResolvedValue(response(40))
    const files = await loadBoardImages('private-workspace', 'board/name', ['opaque-file'], new AbortController().signal)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/pages/board%2Fname/board/files/opaque-file?workspace_id=private-workspace', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(files).toHaveLength(1); expect(files[0].id).toBe('opaque-file'); expect(files[0].dataURL).toMatch(/^data:image\/png;/)
  })
  it('rejects hostile response types and declared/streamed oversize before image decoding', async () => {
    browserImageAPIs()
    for (const reply of [response(1, 'text/html'), response(1, 'image/png', MAX_BOARD_IMAGE_BYTES + 1), response(MAX_BOARD_IMAGE_BYTES + 1)]) {
      fetchMock.mockResolvedValue(reply)
      await expect(loadBoardImages('ws', 'board', ['image'], new AbortController().signal)).rejects.toThrow()
    }
    expect(createImageBitmap).not.toHaveBeenCalled()
  })
  it('rejects unauthorized bytes and aborts concurrent loading', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 403, json: async () => ({ error: 'access revoked' }) } as Response)
    await expect(loadBoardImages('ws', 'board', ['image'], new AbortController().signal)).rejects.toMatchObject({ status: 403 })
  })
  it('does not complete a load after its board generation was cancelled', async () => {
    browserImageAPIs(); const controller = new AbortController()
    fetchMock.mockImplementation(async () => { controller.abort(); return response(40) })
    await expect(loadBoardImages('ws', 'old-board', ['image'], controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(createImageBitmap).not.toHaveBeenCalled()
  })
  it('retains the exact upload mutation/base sequence for failed retries', async () => {
    const file = new File(['png'], 'image.png', { type: 'image/png' }); const signal = new AbortController().signal
    fetchMock.mockResolvedValue({ ok: false, status: 503, json: async () => ({ error: 'retry' }) } as Response)
    for (let i = 0; i < 2; i++) await expect(uploadBoardImage('ws', 'board', file, 7, 'same-mutation', signal)).rejects.toMatchObject({ status: 503 })
    expect(fetchMock.mock.calls[0][0]).toBe(fetchMock.mock.calls[1][0]); expect(fetchMock.mock.calls[0][0]).toContain('base_seq=7&mutation_id=same-mutation')
    expect(fetchMock.mock.calls[0][1]?.body).toBe(file)
  })
})
