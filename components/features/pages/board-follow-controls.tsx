"use client"
import { Button } from '@/components/ui/button'
import type { useBoardFollow } from '@/hooks/use-board-follow'
export function BoardFollowControls({ follow, names, onSelect }: { follow: ReturnType<typeof useBoardFollow>; names: Record<string, string>; onSelect: (connection: string) => void }) {
  const label = (member: typeof follow.eligible[number]) => `${names[member.user_id] || 'Viewer'} · tab ${member.connection_id.slice(-4)}`
  return <div className="flex flex-wrap items-center gap-2 px-4 pb-3"><label className="text-xs text-muted-foreground">Follow cursor<select aria-label="Follow a live viewer cursor" className="ml-2 rounded-md border bg-background p-2 text-sm coarse:min-h-12" value={follow.active?.connection_id ?? ''} disabled={!follow.eligible.length} onChange={event => onSelect(event.target.value)}><option value="">{follow.eligible.length ? 'Choose a live viewer' : 'No other live cursor'}</option>{follow.eligible.map(member => <option key={member.connection_id} value={member.connection_id}>{label(member)}</option>)}</select></label>{follow.active && <><Button variant="outline" size="sm" className="coarse:h-12" onClick={follow.stop}>Stop following</Button><span role="status" className="text-xs text-muted-foreground">Following {label(follow.active)}&apos;s cursor. Interact with the canvas or press Escape to stop.</span></>}</div>
}
