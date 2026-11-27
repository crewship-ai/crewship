"use client"

import { useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { readArtifactSnapshot } from "@/components/features/chat/artifact/artifact-file-io"
import { SheetFromFileRow } from "./sheet-from-file"
import { useSheetPanel } from "./sheet-panel-context"
import { workbookBaseName } from "./workbook-file"

const WORKBOOK_TYPE = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

/** A new Sheet sits beside any Sheet of the same name, never over it. */
export function outputSheetName(path: string): string {
  return `${workbookBaseName(path.split("/").pop() ?? path)} (agent output)`
}

/**
 * An .xlsx among the agent's output files, opened as a Sheet in this chat: the file is read
 * as it is and becomes version 1 of a new Sheet, which opens beside the chat. The file itself does not change.
 */
export function OutputSheetOffer({ workspaceId, agentId, path, chatId, viewerId }: { workspaceId: string; agentId: string; path: string; chatId?: string | null; viewerId?: string | null }) {
  const panel = useSheetPanel()
  const [file, setFile] = useState<File | null>(null)
  const [reading, setReading] = useState(false)
  const [failed, setFailed] = useState(false)
  const abort = useRef<AbortController | null>(null)
  const fileName = path.split("/").pop() ?? path
  const read = async () => {
    abort.current?.abort()
    const controller = abort.current = new AbortController()
    setReading(true); setFailed(false)
    try {
      const snapshot = await readArtifactSnapshot({ agentId, workspaceId, path, signal: controller.signal })
      if (!controller.signal.aborted) setFile(new File([snapshot.bytes as BlobPart], fileName, { type: WORKBOOK_TYPE }))
    } catch { if (!controller.signal.aborted) setFailed(true) }
    finally { if (abort.current === controller) { abort.current = null; setReading(false) } }
  }
  const chat = chatId ?? panel?.chatId ?? undefined
  if (file) return <SheetFromFileRow workspaceId={workspaceId} {...(chat ? { chatId: chat } : {})} {...(viewerId ? { userId: viewerId } : {})}
    file={file} attached sideBySide={Boolean(panel)} sheetName={outputSheetName(path)}
    onOpen={pageId => panel?.open(pageId)} onDismiss={() => setFile(null)} />
  return <div className="flex flex-wrap items-center gap-2">
    <Button type="button" size="xs" variant="outline" disabled={reading} onClick={() => void read()}>Open as a Sheet</Button>
    {reading && <span role="status" className="text-muted-foreground">Reading the file…</span>}
    {failed && <span role="alert" className="text-destructive">The file could not be read, so no Sheet was created. Try again.</span>}
  </div>
}
