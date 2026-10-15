"use client"
import { Button } from "@/components/ui/button"
export function BoardToolbar({ seq, dirty, writable, pending, invalid, onSave, onReload, onPin }: { seq: number; dirty: boolean; writable: boolean; pending: boolean; invalid: boolean; onSave: () => void; onReload: () => void; onPin: () => void }) {
  return <div className="flex flex-wrap items-center justify-between gap-3 p-4"><div><p className="text-sm font-medium">Board · Saved sequence {seq}</p><p className="text-xs text-muted-foreground">{dirty ? "Unsaved drawing changes. Save preserves the revisions you started from." : "Changes are saved explicitly. HTML previews stay static; scripts are disabled."}</p></div><div className="flex flex-wrap gap-2">
    <Button variant="outline" size="sm" className="coarse:h-12" disabled={pending} onClick={onReload}>Reload latest</Button>
    {writable && <><Button variant="outline" size="sm" className="coarse:h-12" disabled={pending || dirty} title={dirty ? "Save or discard drawing changes before pinning." : undefined} onClick={onPin}>Pin artifact</Button><Button size="sm" className="coarse:h-12" disabled={pending || !dirty || invalid} onClick={onSave}>{pending ? "Saving…" : "Save board"}</Button></>}
  </div></div>
}
