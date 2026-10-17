"use client"
import { useEffect, useRef, useState } from 'react'
import type { BinaryFileData } from '@excalidraw/excalidraw/types'
import { BoardImageError, loadBoardImages } from '@/lib/boards/files'

export function useBoardImages(workspace: string, slug: string, pageId: string, ids: readonly string[], onAccessDenied?: () => void) {
  const identity = `${workspace}\0${slug}\0${pageId}`
  const stamp = JSON.stringify([...new Set(ids)].sort())
  const epoch = useRef(0)
  const denied = useRef(onAccessDenied); denied.current = onAccessDenied
  const [state, setState] = useState<{ identity: string; stamp: string; files: BinaryFileData[]; error: string | null; loading: boolean }>({ identity: '', stamp: '', files: [], error: null, loading: false })
  useEffect(() => {
    const generation = ++epoch.current
    const controller = new AbortController()
    setState({ identity, stamp, files: [], error: null, loading: true })
    const current = () => generation === epoch.current && !controller.signal.aborted
    if (!workspace || !slug || !pageId) { setState({ identity, stamp, files: [], error: null, loading: false }); return () => controller.abort() }
    void loadBoardImages(workspace, slug, JSON.parse(stamp) as string[], controller.signal).then(files => {
      if (current()) setState({ identity, stamp, files, error: null, loading: false })
    }).catch(error => {
      controller.abort()
      if (generation !== epoch.current) return
      if (error instanceof DOMException && error.name === 'AbortError') return
      setState({ identity, stamp, files: [], error: error instanceof Error ? error.message : 'Board images unavailable', loading: false })
      if (error instanceof BoardImageError && [401, 403, 404].includes(error.status)) denied.current?.()
    })
    return () => { controller.abort() }
  }, [identity, stamp, workspace, slug, pageId])
  // A new Board must never see a previous Board's data for even one render.
  return state.identity === identity && state.stamp === stamp ? state : { identity, stamp, files: [], error: null, loading: true }
}
