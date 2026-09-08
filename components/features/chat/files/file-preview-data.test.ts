import { describe, it, expect, vi } from 'vitest'
import { previewMime, readPreviewBytes, MAX_PREVIEW_BYTES } from './file-preview-data'
import { apiFetch } from '@/lib/api-fetch'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)

describe('safe preview signatures', () => {
  it.each([
    [[37,80,68,70,45], 'application/pdf'], [[137,80,78,71,13,10,26,10], 'image/png'],
    [[255,216,255,224], 'image/jpeg'], [[82,73,70,70,0,0,0,0,87,69,66,80], 'image/webp'],
    [[60,115,118,103,62], null], [[60,104,116,109,108,62], null], [[137,80], null], [[],null],
  ])('detects only complete allowlisted signatures (%j)', (bytes, mime) => {
    expect(previewMime(new Uint8Array(bytes as number[]))).toBe(mime)
  })
})
describe('bounded authenticated reads', () => {
  it('preserves bytes across chunks and passes abort signal to authenticated fetch', async () => {
    fetchMock.mockResolvedValue(new Response(new ReadableStream({start(c){c.enqueue(new Uint8Array([1,2]));c.enqueue(new Uint8Array([3]));c.close()}})))
    const signal = new AbortController().signal
    expect(await readPreviewBytes('/api/file',signal)).toEqual(new Uint8Array([1,2,3]))
    expect(fetchMock).toHaveBeenLastCalledWith('/api/file',{signal})
  })
  it('rejects oversized declared length before reading', async () => {
    const cancel=vi.fn();fetchMock.mockResolvedValue(new Response(new ReadableStream({cancel}),{headers:{'content-length':String(MAX_PREVIEW_BYTES+1)}}))
    await expect(readPreviewBytes('/api/file',new AbortController().signal)).rejects.toThrow('20 MiB')
    expect(cancel).toHaveBeenCalledOnce()
  })
  it('enforces cap even when Content-Length is missing or lies and cancels stream', async () => {
    const cancel=vi.fn();let calls=0
    fetchMock.mockResolvedValue(new Response(new ReadableStream({pull(c){calls++;c.enqueue(new Uint8Array(MAX_PREVIEW_BYTES/2));},cancel}),{headers:{'content-length':'1'}}))
    await expect(readPreviewBytes('/api/file',new AbortController().signal)).rejects.toThrow('20 MiB')
    expect(cancel).toHaveBeenCalledOnce();expect(calls).toBeLessThanOrEqual(4)
  })
  it('rejects auth failure before consuming error body', async () => {
    fetchMock.mockResolvedValue(new Response('private error',{status:403}))
    await expect(readPreviewBytes('/api/file',new AbortController().signal)).rejects.toThrow('(403)')
  })
  it('does not return bytes after abort', async () => {
    fetchMock.mockResolvedValue(new Response('data'))
    const controller=new AbortController();controller.abort()
    await expect(readPreviewBytes('/api/file',controller.signal)).rejects.toMatchObject({name:'AbortError'})
  })
})
