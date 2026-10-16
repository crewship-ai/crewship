"use client"
import { useCallback, useEffect, useRef, useState } from 'react'
import { apiFetch } from '@/lib/api-fetch'
import type { PageBoard } from '@/hooks/use-page-board'
import { BoardImageError, uploadBoardImage, type BoardImageUpload } from '@/lib/boards/files'

export interface BoardImageIntent { file: File; baseSeq: number; mutationId: string }
export interface BoardImageReady { file: BoardImageUpload; board: PageBoard }
export function useBoardImageUpload(workspace: string, slug: string, pageId: string, onAccessDenied?: () => void) {
  const identity = `${workspace}\0${slug}\0${pageId}`
  const currentIdentity = useRef(identity); currentIdentity.current = identity
  const denied=useRef(onAccessDenied);denied.current=onAccessDenied
 const request = useRef<AbortController | null>(null)
  const [state, setState] = useState({ identity, pending: false, error: null as string | null })
  useEffect(() => () => request.current?.abort(), [identity])
  const upload = useCallback(async (intent: BoardImageIntent): Promise<BoardImageReady | null> => {
    if (request.current && !request.current.signal.aborted) return null
    const controller = new AbortController(); request.current = controller
    const current = () => !controller.signal.aborted && currentIdentity.current === identity
    setState({ identity, pending: true, error: null })
    try {
      const file = await uploadBoardImage(workspace, slug, intent.file, intent.baseSeq, intent.mutationId, controller.signal)
      if (!current()) return null
      // The upload advanced sequence. Adopt only a fresh authorized snapshot,
      // never rewrite an uncertain drawing draft's preconditions from the reply.
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/board?workspace_id=${encodeURIComponent(workspace)}`, { signal: controller.signal })
      if (!response.ok) throw new BoardImageError('Image uploaded; reload the board before placing it.', response.status)
      const board = await response.json() as PageBoard
      if (!current()) return null
      if (!board.can_write) throw new BoardImageError("Image uploaded; board write permission changed.",403)
      if (board.format!=="crewship-board-excalidraw-0.18.1-v1" || !Number.isSafeInteger(board.seq) || board.seq < file.result_seq || !Array.isArray(board.records) || !Array.isArray(board.placements)) throw new BoardImageError('Image uploaded; canonical board snapshot unavailable.')
      setState({ identity, pending: false, error: null })
      return { file, board }
    } catch (error) {
      if (current()) {setState({ identity, pending: false, error: error instanceof Error ? error.message : 'Image upload unavailable; retry the same image.' });if(error instanceof BoardImageError&&[401,403,404].includes(error.status))denied.current?.()}
      return null
    } finally { controller.abort(); if (request.current === controller) request.current = null }
  }, [identity, workspace, slug])
  return { upload, ...(state.identity === identity ? state : { identity, pending: false, error: null }) }
}
