"use client"
import { useCallback, useEffect, useRef } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api-fetch'
import { pagesKeys } from './use-pages'
export interface ArtifactCopyInput { source_revision: number; slug: string; name: string }
export class ArtifactCopyError extends Error {
  constructor(message: string, public status?: number) { super(message) }
  get uncertain() { return this.status === undefined || this.status === 408 || this.status >= 500 }
}
export function useArtifactCopy(workspace: string, sourceSlug: string) {
  // Only digests and opaque retry IDs survive an access-loss reset, not names.
  const pending = useRef(new Map<string,string>())
  const controllers = useRef(new Set<AbortController>())
  const generation = useRef({value:0})
  useEffect(() => { const active = controllers.current; const epoch = generation.current; pending.current.clear(); return () => { epoch.value++; for (const controller of active) controller.abort(); active.clear() } }, [workspace,sourceSlug])
  const client = useQueryClient()
  const mutation = useMutation({ gcTime: 0, mutationFn: async (input: ArtifactCopyInput) => {
    const currentGeneration = generation.current.value
    const bytes = await crypto.subtle.digest('SHA-256',new TextEncoder().encode(JSON.stringify({workspace,sourceSlug,source_revision:input.source_revision,slug:input.slug,name:input.name})))
    const fingerprint = Array.from(new Uint8Array(bytes),byte => byte.toString(16).padStart(2,'0')).join('')
    if (!pending.current.has(fingerprint)) {
      if (pending.current.size >= 32) throw new ArtifactCopyError('Retry an earlier unconfirmed copy before creating another.',409)
      pending.current.set(fingerprint,crypto.randomUUID())
    }
    if (currentGeneration !== generation.current.value) throw new ArtifactCopyError('Copy was cancelled.',409)
    const controller = new AbortController()
    controllers.current.add(controller)
    let response: Response
    try { response = await apiFetch(`/api/v1/pages/${encodeURIComponent(sourceSlug)}/artifact/copies?workspace_id=${encodeURIComponent(workspace)}`, {signal:controller.signal,method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...input,mutation_id:pending.current.get(fingerprint)})}) }
    catch { throw new ArtifactCopyError('The copy could not be confirmed. Retry the same copy to check its result.') }
    finally { controllers.current.delete(controller) }
    if (currentGeneration !== generation.current.value) throw new ArtifactCopyError('Copy was cancelled.',409)
    const body = await response.json().catch(() => null)
    if (currentGeneration !== generation.current.value) throw new ArtifactCopyError('Copy was cancelled.',409)
    if (!response.ok) {
      const error = new ArtifactCopyError(body?.error ?? 'Could not copy this artifact.',response.status)
      if (!error.uncertain) pending.current.delete(fingerprint)
      throw error
    }
    if (typeof body?.page_slug !== 'string' || body.revision !== 1) throw new ArtifactCopyError('The copy response could not be confirmed. Retry the same copy.')
    await client.invalidateQueries({queryKey:pagesKeys.all(workspace)})
    if (currentGeneration !== generation.current.value) throw new ArtifactCopyError('Copy was cancelled.',409)
    pending.current.delete(fingerprint)
    return body as {page_slug:string;revision:number}
  } })
  const reset = mutation.reset
  const cancel = useCallback(() => { generation.current.value++; for (const controller of controllers.current) controller.abort(); controllers.current.clear(); reset() }, [reset])
  return { ...mutation, cancel }
}
