"use client"

import { diffBlocks, summarizeDiff, type BlockDiff, type DiffKind } from "@/lib/documents/block-diff"

const LABEL: Record<DiffKind, string> = { same: "Unchanged", added: "Added", removed: "Removed", changed: "Changed", moved: "Moved" }

function Entry({ entry }: { entry: BlockDiff }) {
  // The kind is spelled out, so the state never depends on colour alone.
  return <li className="space-y-1 border-l-2 pl-3" data-kind={entry.kind}>
    <p className="text-sm font-medium">{LABEL[entry.kind]} · {entry.type}</p>
    {entry.before !== undefined && entry.kind !== "moved" && <p className="whitespace-pre-wrap break-words text-sm line-through decoration-1"><span className="sr-only">Before: </span>{entry.before || "(empty)"}</p>}
    {entry.beforeFormat !== undefined && <p className="break-words text-sm"><span className="font-medium">Formatting before:</span> {entry.beforeFormat}</p>}
    {entry.afterFormat !== undefined && <p className="break-words text-sm"><span className="font-medium">Formatting after:</span> {entry.afterFormat}</p>}
    {entry.after !== undefined && entry.kind !== "removed" && <p className="whitespace-pre-wrap break-words text-sm"><span className="sr-only">After: </span>{entry.after || "(empty)"}</p>}
  </li>
}

/** Read-only block comparison of two confirmed contents. Never touches the live editor. */
export function DocumentCompare({ base, target, baseLabel, targetLabel }: { base: unknown; target: unknown; baseLabel: string; targetLabel: string }) {
  const diff = diffBlocks(base, target)
  const counts = summarizeDiff(diff)
  const changes = diff.filter(entry => entry.kind !== "same")
  return <section aria-label={`Compare ${baseLabel} with ${targetLabel}`} className="space-y-2">
    <p role="status" className="text-sm">
      {changes.length === 0 ? `${baseLabel} and ${targetLabel} have the same content.`
        : `${targetLabel} compared with ${baseLabel}: ${counts.added} added, ${counts.removed} removed, ${counts.changed} changed, ${counts.moved} moved.`}
    </p>
    {changes.length > 0 && <ul className="max-h-80 space-y-3 overflow-y-auto">{changes.map(entry => <Entry key={`${entry.kind}:${entry.id}`} entry={entry} />)}</ul>}
  </section>
}
