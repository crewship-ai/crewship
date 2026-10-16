"use client"
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { PresenceMember } from './use-content-presence'
export function useBoardFollow(members: PresenceMember[], currentUserId: string | undefined, available: boolean, seq: number, enabled: boolean) {
  const [selection, setSelection] = useState<{ connection: string; seq: number } | null>(null)
  const eligible = useMemo(() => available && enabled ? members.filter(member => member.user_id !== currentUserId && member.board_seq === seq && member.cursor?.coordinate_space === 'board') : [], [members, currentUserId, available, seq, enabled])
  const active = selection?.seq === seq ? eligible.find(member => member.connection_id === selection.connection) : undefined
  const stop = useCallback(() => setSelection(null), [])
  useEffect(() => { if (selection && !active) setSelection(null) }, [selection, active])
  const select = useCallback((connection: string) => {
    if (eligible.some(member => member.connection_id === connection)) setSelection({ connection, seq })
    else setSelection(null)
  }, [eligible, seq])
  const x = active?.cursor?.x, y = active?.cursor?.y, connection = active?.connection_id
  const focusPoint = useMemo(() => connection && x !== undefined && y !== undefined ? { x, y, requestId: `follow:${connection}:${seq}:${x}:${y}` } : undefined, [connection, seq, x, y])
  return { eligible, active, focusPoint, select, stop }
}
